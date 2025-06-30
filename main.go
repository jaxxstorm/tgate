// main.go
package main

import (
	"context"
	"embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/jaxxstorm/tgate/internal/config"
	"github.com/jaxxstorm/tgate/internal/httputil"
	"github.com/jaxxstorm/tgate/internal/logging"
	"github.com/jaxxstorm/tgate/internal/model"
	"github.com/jaxxstorm/tgate/internal/proxy"
	"github.com/jaxxstorm/tgate/internal/server"
	"github.com/jaxxstorm/tgate/internal/tailscale"
	"github.com/jaxxstorm/tgate/internal/tui"
	"github.com/jaxxstorm/tgate/internal/ui"
)

//go:embed ui/*
var uiFiles embed.FS

// Version will be set by goreleaser
var Version = "dev"

func main() {
	cfg, err := config.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Handle version flag
	if cfg.Version {
		fmt.Printf("tgate version %s\n", Version)
		os.Exit(0)
	}

	// Handle cleanup flag
	if cfg.CleanupServe {
		handleCleanupServe()
		os.Exit(0)
	}

	// Setup initial logger
	logConfig := logging.Config{
		Verbose: cfg.Verbose,
		JSON:    cfg.JSON,
		LogFile: cfg.LogFile,
	}

	logger, err := logging.SetupLogger(logConfig)
	if err != nil {
		fmt.Printf("Failed to setup logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	startTime := time.Now()

	logger.Info(logging.MsgServerStarting,
		logging.Component("tgate"),
		logging.Version(Version),
	)

	// Server mode determination
	var serverMode model.ServerMode
	if cfg.Mock {
		serverMode = model.ModeMock
		logger.Info(logging.MsgMockModeConfiguration,
			logging.MockMode(true),
			logging.Status("auto_enabling_funnel"),
		)
	} else {
		serverMode = model.ModeProxy
	}

	// Auto-configure options
	if cfg.Funnel {
		logger.Info(logging.MsgTailscaleFunnelEnabled,
			logging.Status("auto_enabling_https"),
		)
	}

	// Log configuration
	logger.Info(logging.MsgServerConfiguration,
		logging.ServerMode(serverMode.String()),
		logging.TargetPort(cfg.Port),
		logging.FunnelEnabled(cfg.Funnel),
		logging.HTTPSEnabled(cfg.UseHTTPS),
		logging.UIEnabled(!cfg.NoUI),
		logging.TUIEnabled(!cfg.NoTUI),
		logging.MockMode(cfg.Mock),
	)

	// Test local connection only in proxy mode
	if !cfg.Mock {
		logger.Info(logging.MsgConnectionTesting,
			logging.TargetPort(cfg.Port),
		)

		testConn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", cfg.Port), 5*time.Second)
		if err != nil {
			logger.Fatal(logging.MsgConnectionFailed,
				logging.TargetPort(cfg.Port),
				logging.Error(err),
			)
		}
		testConn.Close()

		logger.Info(logging.MsgConnectionSuccess,
			logging.TargetPort(cfg.Port),
		)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create proxy server
	proxyConfig := proxy.Config{
		TargetPort: cfg.Port,
		UseTUI:     !cfg.NoTUI,
		Mode:       serverMode,
		Logger:     logger,
	}

	proxyServer := proxy.NewServer(proxyConfig)

	// Determine which Tailscale mode to use
	useLocalTailscale := false
	var tsClient *tailscale.Client

	if !cfg.ForceTsnet && cfg.AuthKey == "" {
		// Try to use local Tailscale - pass logger instead of sugar
		tsClient = tailscale.NewClient(logger)
		if tsClient.IsAvailable(ctx) {
			useLocalTailscale = true
			logger.Info(logging.MsgTailscaleDetected,
				logging.TailscaleMode("local_daemon"),
			)
		} else {
			logger.Info(logging.MsgTailscaleNotAvailable,
				logging.Status("falling_back_to_tsnet"),
			)
		}
	}

	if cfg.AuthKey != "" {
		logger.Info(logging.MsgTailscaleConfiguration,
			logging.TailscaleMode("tsnet"),
			logging.Status("auth_key_provided"),
		)
	}

	if cfg.ForceTsnet {
		logger.Info(logging.MsgTailscaleConfiguration,
			logging.TailscaleMode("tsnet"),
			logging.Status("forced"),
		)
	}

	if cfg.NoTUI {
		runWithoutTUI(ctx, logger, useLocalTailscale, tsClient, proxyServer, cfg)
	} else {
		runWithTUI(ctx, logger, useLocalTailscale, tsClient, proxyServer, cfg)
	}

	logger.Info(logging.MsgServerStopped,
		logging.Duration(time.Since(startTime)),
	)
}

func runWithoutTUI(ctx context.Context, logger *zap.Logger, useLocalTailscale bool, tsClient *tailscale.Client, proxyServer *proxy.Server, cfg *config.Config) {
	logger.Info(logging.MsgConsoleMode,
		logging.TUIEnabled(false),
	)

	// Display running information (legacy mode)
	fmt.Print("\n" + strings.Repeat("─", 60) + "\n")
	if useLocalTailscale {
		fmt.Printf("  tgate is running with Tailscale serve!\n")
		fmt.Printf("  Mode: Local Tailscale daemon\n")
	} else {
		fmt.Printf("  tgate is running with tsnet!\n")
		fmt.Printf("  Mode: tsnet device (%s)\n", cfg.TailscaleName)
	}
	if cfg.Mock {
		fmt.Printf("  Mode: Mock/Public\n")
	} else {
		fmt.Printf("  Target: localhost:%d\n", cfg.Port)
	}
	if !cfg.NoUI {
		fmt.Printf("  Web UI: Available via Tailscale\n")
	}
	fmt.Print(strings.Repeat("─", 60) + "\n\n")

	// Set up servers after displaying info
	var cleanup func() error
	var uiCleanup func() error
	var serviceInfo *tailscale.ServiceInfo

	if useLocalTailscale {
		cleanup, uiCleanup, serviceInfo = setupLocalTailscale(ctx, tsClient, proxyServer, logger, cfg)
	} else {
		cleanup = setupTsnet(ctx, proxyServer, logger, cfg)
	}

	// Display service URLs if available
	if serviceInfo != nil {
		fmt.Print("\n" + strings.Repeat("═", 60) + "\n")
		fmt.Printf("  🚀 SERVICE READY\n")
		fmt.Print(strings.Repeat("─", 60) + "\n")

		if serviceInfo.IsFunnel {
			fmt.Printf("  🌍 Internet Access:  %s\n", serviceInfo.URL)
			fmt.Printf("     (Available to anyone on the internet)\n")
		} else {
			fmt.Printf("  🔒 Tailnet Access:   %s\n", serviceInfo.URL)
			fmt.Printf("     (Available to your Tailscale network only)\n")
		}

		fmt.Printf("  🏠 Local Testing:    %s\n", serviceInfo.LocalURL)
		fmt.Printf("     (For local development and testing)\n")

		if !cfg.NoUI && len(proxyServer.GetWebUIURL()) > 0 {
			fmt.Printf("  📊 Web UI:           %s\n", proxyServer.GetWebUIURL())
			fmt.Printf("     (Request logs and statistics)\n")
		}

		fmt.Print(strings.Repeat("═", 60) + "\n\n")

		fmt.Printf("Press Ctrl+C to stop the server\n\n")
	}

	logger.Info(logging.MsgSetupComplete,
		logging.TailscaleMode(func() string {
			if useLocalTailscale {
				return "local_daemon"
			} else {
				return "tsnet"
			}
		}()),
	)

	// Wait for shutdown signal
	<-ctx.Done()

	logger.Info(logging.MsgServerStopping)

	if cleanup != nil {
		if err := cleanup(); err != nil {
			logger.Error(logging.MsgRuntimeError,
				logging.Operation("cleanup"),
				logging.Error(err),
			)
		}
	}

	if uiCleanup != nil {
		if err := uiCleanup(); err != nil {
			logger.Error(logging.MsgRuntimeError,
				logging.Operation("ui_cleanup"),
				logging.Error(err),
			)
		}
	}
}

func runWithTUI(ctx context.Context, logger *zap.Logger, useLocalTailscale bool, tsClient *tailscale.Client, proxyServer *proxy.Server, cfg *config.Config) {
	// TUI MODE - Initialize TUI with proper message routing

	// Create TUI program
	tuiModel := tui.NewModel(proxyServer)
	program := tea.NewProgram(tuiModel, tea.WithAltScreen())

	// Connect the proxy server to the TUI immediately
	proxyServer.SetProgram(program)

	// Add a single listener that properly converts and sends messages to TUI
	proxyServer.AddListener(func(log model.RequestLog) {
		// Send using the correct TUI message type
		program.Send(tui.RequestMsg{Log: log})
	})

	// Replace the server's logger to route to TUI instead of console
	tuiZapLogger := tui.CreateTUIZapLogger(program)
	proxyServer.ReplaceLogger(tuiZapLogger)

	// Set up servers in background
	var cleanup func() error
	var uiCleanup func() error

	// Start server setup in a goroutine
	go func() {
		// Wait a moment for TUI to initialize
		time.Sleep(500 * time.Millisecond)

		// Create a custom logger that only sends to TUI
		tuiOnlyLogger := tui.NewTUIOnlyLogger(program)

		// Send initial messages to TUI application logs using the logger for consistency
		tuiOnlyLogger.Infof("TUI initialization completed successfully mode=interactive")
		tuiOnlyLogger.Infof("Control commands available quit_key=q alternate_quit=Ctrl+C navigation=arrow_keys,j,k scroll=PgUp,PgDn")

		tuiOnlyLogger.Infof("Server setup starting mode=%s target_port=%d funnel_enabled=%t https_enabled=%t ui_enabled=%t",
			func() string {
				if cfg.Mock {
					return "mock"
				} else {
					return "proxy"
				}
			}(),
			cfg.Port, cfg.Funnel, cfg.UseHTTPS, !cfg.NoUI)

		// Create a new Tailscale client with a simple TUI logger for TUI mode
		var tuiTsClient *tailscale.Client
		if useLocalTailscale {
			// Use a simple zap logger that directly routes to TUIOnlyLogger
			simpleTUIZapLogger := tui.CreateSimpleTUIZapLogger(tuiOnlyLogger)
			tuiTsClient = tailscale.NewClient(simpleTUIZapLogger)
			// Verify it's still available with the new client
			if !tuiTsClient.IsAvailable(ctx) {
				tuiOnlyLogger.Errorf("Tailscale not available in TUI mode")
				return
			}
		}

		if useLocalTailscale {
			cleanup, uiCleanup = server.SetupLocalTailscaleQuiet(ctx, tuiTsClient, proxyServer, tuiOnlyLogger, cfg, uiFiles)
		} else {
			cleanup = server.SetupTsnetQuiet(ctx, proxyServer, tuiOnlyLogger, cfg)
		}

		tuiOnlyLogger.Infof("Server setup completed successfully total_duration=%dms tailscale_mode=%s",
			time.Since(time.Now().Add(-2*time.Second)).Milliseconds(), // rough estimate
			func() string {
				if useLocalTailscale {
					return "local_daemon"
				} else {
					return "tsnet"
				}
			}())

		// Log web UI URL if available
		if !cfg.NoUI && proxyServer.GetWebUIURL() != "" {
			tuiOnlyLogger.Infof("Web dashboard available url=%s ui_port=%d tailscale_port=auto accessibility=tailnet_only",
				proxyServer.GetWebUIURL(), cfg.UIPort)
		}
	}()

	// Run TUI - this blocks until user quits
	if _, err := program.Run(); err != nil {
		fmt.Printf("TUI error: %v\n", err)
	}

	// Cleanup after TUI exits
	if cleanup != nil {
		if err := cleanup(); err != nil {
			fmt.Printf("Error during cleanup: %v\n", err)
		}
	}

	if uiCleanup != nil {
		if err := uiCleanup(); err != nil {
			fmt.Printf("Error during UI cleanup: %v\n", err)
		}
	}

	fmt.Printf("tgate server stopped\n")
}

func setupLocalTailscale(ctx context.Context, tsClient *tailscale.Client, proxyServer *proxy.Server, logger *zap.Logger, cfg *config.Config) (cleanup func() error, uiCleanup func() error, serviceInfo *tailscale.ServiceInfo) {
	// Find an available port for our local proxy server using random allocation
	logger.Info("Allocating random proxy port",
		logging.Component("proxy_server"),
	)

	proxyPort, err := tailscale.FindAvailableLocalPort()
	if err != nil {
		logger.Fatal("Failed to allocate proxy port",
			logging.Component("proxy_server"),
			logging.Error(err),
		)
	}

	logger.Info(logging.MsgPortAllocated,
		logging.Component("proxy_server"),
		logging.ProxyPort(proxyPort),
	)

	// Start proxy server
	logger.Info(logging.MsgProxyStarting,
		logging.ProxyPort(proxyPort),
		logging.BindAddress("0.0.0.0"),
	)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", proxyPort),
		Handler: proxyServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(logging.MsgRuntimeError,
				logging.Component("proxy_server"),
				logging.ProxyPort(proxyPort),
				logging.Error(err),
			)
		}
	}()

	// Wait for the server to be ready
	if err := httputil.WaitForServerReady(ctx, fmt.Sprintf("localhost:%d", proxyPort), 2*time.Second); err != nil {
		logger.Error("Proxy server failed to start",
			logging.Component("proxy_server"),
			logging.ProxyPort(proxyPort),
			logging.Error(err),
		)
		return cleanup, uiCleanup, nil
	}

	logger.Info(logging.MsgProxyStarted,
		logging.ProxyPort(proxyPort),
	)

	// Set up UI server if enabled
	if !cfg.NoUI {
		uiPort := cfg.UIPort
		if uiPort == 0 {
			logger.Info("Allocating random UI port",
				logging.Component("ui_server"),
			)

			uiPort, err = tailscale.FindAvailableLocalPort()
			if err != nil {
				logger.Warn(logging.MsgPortAllocationFailed,
					logging.Component("ui_server"),
					logging.Status("disabling_ui"),
					logging.Error(err),
				)
			}
		}

		if uiPort > 0 {
			logger.Info(logging.MsgUIStarting,
				logging.UIPort(uiPort),
			)

			uiInfo, err := setupUIServer(ctx, tsClient, uiPort, proxyServer, logger)
			if err != nil {
				logger.Warn(logging.MsgSetupFailed,
					logging.Component("ui_server"),
					logging.Status("continuing_without_ui"),
					logging.Error(err),
				)
			} else {
				logger.Info(logging.MsgUIAvailable,
					logging.UIPort(uiPort),
					logging.URL(uiInfo.URL),
				)
				proxyServer.SetWebUIURL(uiInfo.URL)
				uiCleanup = func() error {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return uiInfo.Server.Shutdown(shutdownCtx)
				}
			}
		}
	} else {
		logger.Info(logging.MsgUIDisabled)
	}

	// Set up Tailscale serve
	logger.Info(logging.MsgTailscaleServeSetup,
		logging.MountPath(cfg.GetSetPath()),
		logging.FunnelEnabled(cfg.Funnel),
		logging.HTTPSEnabled(cfg.UseHTTPS),
		logging.ServePort(cfg.GetServePort()),
	)

	tsConfig := tailscale.Config{
		MountPath:    cfg.GetSetPath(),
		EnableFunnel: cfg.Funnel,
		UseHTTPS:     cfg.UseHTTPS,
		ServePort:    cfg.GetServePort(),
		ProxyPort:    proxyPort,
	}

	svcInfo, err := tsClient.SetupServe(ctx, tsConfig)
	if err != nil {
		logger.Fatal(logging.MsgSetupFailed,
			logging.Component("tailscale_serve"),
			logging.Error(err),
		)
	}

	// Display service accessibility information
	logger.Info(logging.MsgServiceReady,
		logging.Component("tgate_service"),
		logging.URL(svcInfo.URL),
	)

	if svcInfo.IsFunnel {
		logger.Info(logging.MsgInternetAccess,
			logging.Component("accessibility"),
			logging.URL(svcInfo.URL),
			logging.Status("public_internet"),
		)
	} else {
		logger.Info(logging.MsgTailnetAccess,
			logging.Component("accessibility"),
			logging.URL(svcInfo.URL),
			logging.Status("tailnet_only"),
		)
	}

	logger.Info(logging.MsgLocalAccess,
		logging.Component("accessibility"),
		logging.URL(svcInfo.LocalURL),
		logging.Status("localhost_testing"),
	)

	logger.Info(logging.MsgTailscaleServeSuccess,
		logging.ProxyPort(proxyPort),
		logging.TargetPort(cfg.Port),
		logging.MockMode(cfg.Mock),
	)

	cleanup = func() error {
		logger.Info(logging.MsgCleanupStarting,
			logging.Component("tailscale_serve"),
		)

		// Use a fresh context for cleanup to avoid cancellation issues
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		// Comprehensive cleanup of all Tailscale serve configs
		err := tsClient.CleanupAll(cleanupCtx)
		if err != nil {
			logger.Warn("Failed to perform comprehensive cleanup, trying specific cleanup",
				logging.Component("tailscale_serve"),
				logging.Error(err),
			)
			// Fallback to specific config cleanup
			tsClient.Cleanup(cleanupCtx, tsConfig)
		}

		// Shutdown proxy server
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(logging.MsgRuntimeError,
				logging.Operation("proxy_shutdown"),
				logging.Error(err),
			)
			return err
		}

		logger.Info(logging.MsgCleanupComplete,
			logging.Component("proxy_server"),
		)
		return nil
	}

	return cleanup, uiCleanup, svcInfo
}

// handleCleanupServe clears all Tailscale serve configurations
func handleCleanupServe() {
	ctx := context.Background()

	// Setup basic logger for cleanup operation
	logConfig := logging.Config{
		Verbose: true,
		JSON:    false,
	}

	logger, err := logging.SetupLogger(logConfig)
	if err != nil {
		fmt.Printf("Failed to setup logger for cleanup: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("Starting Tailscale serve cleanup operation",
		logging.Component("cleanup"),
	)

	// Create Tailscale client
	tsClient := tailscale.NewClient(logger)

	// Check if Tailscale is available
	if !tsClient.IsAvailable(ctx) {
		logger.Error("Tailscale daemon not available for cleanup",
			logging.Component("cleanup"),
		)
		fmt.Printf("Error: Tailscale daemon not available. Please ensure Tailscale is running.\n")
		os.Exit(1)
	}

	// Perform comprehensive cleanup
	err = tsClient.CleanupAll(ctx)
	if err != nil {
		logger.Error("Failed to cleanup Tailscale serve configurations",
			logging.Component("cleanup"),
			logging.Error(err),
		)
		fmt.Printf("Error: Failed to cleanup Tailscale serve configurations: %v\n", err)
		os.Exit(1)
	}

	logger.Info("Tailscale serve cleanup completed successfully",
		logging.Component("cleanup"),
	)
	fmt.Printf("✅ All Tailscale serve configurations have been cleared.\n")
	fmt.Printf("You can verify with: tailscale serve status\n")
}

func setupTsnet(ctx context.Context, proxyServer *proxy.Server, logger *zap.Logger, cfg *config.Config) func() error {
	logger.Info("Setting up TSNet mode",
		logging.Component("tsnet_setup"),
		logging.TailscaleMode("tsnet"),
		logging.NodeName(cfg.TailscaleName),
	)

	// Use tsnet mode
	tsnetConfig := tailscale.TSNetConfig{
		Hostname: cfg.TailscaleName,
		AuthKey:  cfg.AuthKey,
	}

	// Pass the zap.Logger directly instead of creating a sugared logger
	tsnetServer := tailscale.NewTSNetServer(tsnetConfig, logger)

	go func() {
		if err := tsnetServer.Serve(ctx, proxyServer); err != nil {
			logger.Error(logging.MsgRuntimeError,
				logging.Component("tsnet_server"),
				logging.Error(err),
			)
		}
	}()

	return func() error {
		logger.Info(logging.MsgCleanupStarting,
			logging.Component("tsnet_server"),
		)

		err := tsnetServer.Close()
		if err != nil {
			logger.Error(logging.MsgRuntimeError,
				logging.Component("tsnet_server"),
				logging.Operation("close"),
				logging.Error(err),
			)
		} else {
			logger.Info(logging.MsgCleanupComplete,
				logging.Component("tsnet_server"),
			)
		}
		return err
	}
}

func setupUIServer(ctx context.Context, tsClient *tailscale.Client, uiPort int, proxyServer *proxy.Server, logger *zap.Logger) (*model.UIServerInfo, error) {
	// Create UI server with the proxy server as the log provider
	uiServer := ui.NewServer(proxyServer, uiFiles)

	// Set up Tailscale serve for UI
	tailscalePort, uiURL, err := tsClient.SetupUIServe(ctx, uiPort)
	if err != nil {
		return nil, fmt.Errorf("failed to setup UI Tailscale serve: %w", err)
	}

	// Start UI server on local port
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", uiPort),
		Handler: uiServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(logging.MsgRuntimeError,
				logging.Component("ui_server"),
				logging.UIPort(uiPort),
				logging.Error(err),
			)
		}
	}()

	// Wait for the UI server to be ready
	if err := httputil.WaitForServerReady(ctx, fmt.Sprintf("localhost:%d", uiPort), 2*time.Second); err != nil {
		logger.Error("UI server failed to start",
			logging.Component("ui_server"),
			logging.UIPort(uiPort),
			logging.Error(err),
		)
		return nil, fmt.Errorf("UI server failed to start: %w", err)
	}

	logger.Info(logging.MsgUIStarted,
		logging.LocalPort(uiPort),
		logging.URL(uiURL),
	)

	return &model.UIServerInfo{
		Server:        httpServer,
		TailscalePort: tailscalePort,
		LocalPort:     uiPort,
		URL:           uiURL,
	}, nil
}
