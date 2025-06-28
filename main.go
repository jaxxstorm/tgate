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

	"github.com/alecthomas/kong"
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"

	"github.com/jaxxstorm/tgate/internal/logging"
	"github.com/jaxxstorm/tgate/internal/model"
	"github.com/jaxxstorm/tgate/internal/proxy"
	"github.com/jaxxstorm/tgate/internal/tailscale"
	"github.com/jaxxstorm/tgate/internal/tui"
	"github.com/jaxxstorm/tgate/internal/ui"
)

//go:embed ui/*
var uiFiles embed.FS

// Version will be set by goreleaser
var Version = "dev"

var CLI struct {
	Port          int    `kong:"arg,optional,help='Local port to expose'"`
	TailscaleName string `kong:"short='n',default='tgate',help='Tailscale node name (only used with tsnet mode)'"`
	Funnel        bool   `kong:"short='f',help='Enable Tailscale funnel (public internet access)'"`
	Verbose       bool   `kong:"short='v',help='Enable verbose logging'"`
	JSON          bool   `kong:"short='j',help='Output logs in JSON format'"`
	LogFile       string `kong:"help='Log file path (optional)'"`
	AuthKey       string `kong:"help='Tailscale auth key to create separate tsnet device'"`
	ForceTsnet    bool   `kong:"help='Force tsnet mode even if local Tailscale is available'"`
	SetPath       string `kong:"help='Set custom path for serve (default: /)'"`
	ServePort     int    `kong:"help='Tailscale serve port (default: 80 for HTTP, 443 for HTTPS)'"`
	UseHTTPS      bool   `kong:"help='Use HTTPS instead of HTTP for Tailscale serve'"`
	NoTUI         bool   `kong:"help='Disable TUI and use simple console output'"`
	NoUI          bool   `kong:"help='Disable web UI dashboard'"`
	UIPort        int    `kong:"help='Custom port for web UI (default: auto-assigned)'"`
	Version       bool   `kong:"help='Show version information'"`
	Mock          bool   `kong:"short='m',help='Enable mock/testing mode (no backing server required, enables funnel by default)'"`
}

func main() {
	kong.Parse(&CLI)

	// Handle version flag
	if CLI.Version {
		fmt.Printf("tgate version %s\n", Version)
		os.Exit(0)
	}

	// Validate arguments
	if CLI.Mock && CLI.Port != 0 {
		fmt.Fprintf(os.Stderr, "Error: Cannot specify both port and --mock flag\n")
		fmt.Fprintf(os.Stderr, "Usage: tgate <port> [flags]     (proxy mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --mock [flags]     (mock/testing mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --version\n")
		os.Exit(1)
	}

	if !CLI.Mock && CLI.Port == 0 {
		fmt.Fprintf(os.Stderr, "Error: port argument is required (or use --mock for testing mode)\n")
		fmt.Fprintf(os.Stderr, "Usage: tgate <port> [flags]     (proxy mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --mock [flags]     (mock/testing mode)\n")
		fmt.Fprintf(os.Stderr, "       tgate --version\n")
		os.Exit(1)
	}

	// Auto-enable funnel for mock mode unless explicitly disabled
	if CLI.Mock && !CLI.Funnel {
		CLI.Funnel = true
		CLI.UseHTTPS = true
	}

	// If funnel is enabled, automatically enable HTTPS since funnel requires it
	if CLI.Funnel {
		CLI.UseHTTPS = true
	}

	// Setup initial logger
	logConfig := logging.Config{
		Verbose: CLI.Verbose,
		JSON:    CLI.JSON,
		LogFile: CLI.LogFile,
	}

	logger, err := logging.SetupLogger(logConfig)
	if err != nil {
		fmt.Printf("Failed to setup logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	sugar := logger.Sugar()

	if CLI.Mock {
		sugar.Infof("🎭 Mock mode enabled - automatically enabling funnel for external access")
	}

	if CLI.Funnel {
		sugar.Infof("🌍 Funnel enabled - automatically enabling HTTPS")
	}

	// Create server mode
	var serverMode model.ServerMode
	if CLI.Mock {
		serverMode = model.ModeMock
	} else {
		serverMode = model.ModeProxy
	}

	sugar.Infof("Starting tgate server...")
	if CLI.Mock {
		sugar.Infof("Mode: Mock/testing (no backing server)")
	} else {
		sugar.Infof("Local target: localhost:%d", CLI.Port)
	}
	sugar.Infof("Funnel enabled: %t", CLI.Funnel)
	sugar.Infof("HTTPS enabled: %t", CLI.UseHTTPS)
	sugar.Infof("Web UI enabled: %t", !CLI.NoUI)

	// Test local connection only in proxy mode
	if !CLI.Mock {
		testConn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", CLI.Port), 5*time.Second)
		if err != nil {
			sugar.Fatalf("Cannot connect to local server at localhost:%d - %v", CLI.Port, err)
		}
		testConn.Close()
		sugar.Infof("✓ Local server is reachable")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create proxy server
	proxyConfig := proxy.Config{
		TargetPort: CLI.Port,
		UseTUI:     !CLI.NoTUI,
		Mode:       serverMode,
		Logger:     logger,
	}

	proxyServer := proxy.NewServer(proxyConfig)

	// Determine which Tailscale mode to use
	useLocalTailscale := false
	var tsClient *tailscale.Client

	if !CLI.ForceTsnet && CLI.AuthKey == "" {
		// Try to use local Tailscale
		tsClient = tailscale.NewClient(sugar)
		if tsClient.IsAvailable(ctx) {
			useLocalTailscale = true
			sugar.Infof("✓ Using local Tailscale daemon")
		} else {
			sugar.Infof("Local Tailscale not available, falling back to tsnet mode")
		}
	}

	if CLI.AuthKey != "" {
		sugar.Infof("Auth key provided - using tsnet mode")
	}

	if CLI.ForceTsnet {
		sugar.Infof("Forced tsnet mode")
	}

	if CLI.NoTUI {
		// Display running information (legacy mode)
		fmt.Printf("\n" + strings.Repeat("─", 60) + "\n")
		if useLocalTailscale {
			fmt.Printf("  tgate is running with Tailscale serve!\n")
			fmt.Printf("  Mode: Local Tailscale daemon\n")
		} else {
			fmt.Printf("  tgate is running with tsnet!\n")
			fmt.Printf("  Mode: tsnet device (%s)\n", CLI.TailscaleName)
		}
		if CLI.Mock {
			fmt.Printf("  Mode: Mock/Public\n")
		} else {
			fmt.Printf("  Target: localhost:%d\n", CLI.Port)
		}
		if !CLI.NoUI {
			fmt.Printf("  Web UI: Available via Tailscale\n")
		}
		fmt.Printf(strings.Repeat("─", 60) + "\n\n")

		// Set up servers after displaying info
		var cleanup func() error
		var uiCleanup func() error

		if useLocalTailscale {
			cleanup, uiCleanup = setupLocalTailscale(ctx, tsClient, proxyServer, logger, sugar)
		} else {
			cleanup = setupTsnet(ctx, proxyServer, logger, sugar)
		}

		// Wait for shutdown signal
		<-ctx.Done()

		sugar.Infof("Shutting down tgate server...")

		if cleanup != nil {
			if err := cleanup(); err != nil {
				sugar.Errorf("Error during cleanup: %v", err)
			}
		}

		if uiCleanup != nil {
			if err := uiCleanup(); err != nil {
				sugar.Errorf("Error during UI cleanup: %v", err)
			}
		}

		sugar.Infof("tgate server stopped")
	} else {
		// Initialize TUI FIRST - disable console logging completely
		
		// Create TUI program
		tuiModel := tui.NewModel(proxyServer)
		program := tea.NewProgram(tuiModel, tea.WithAltScreen())

		// Connect the server to the TUI immediately
		proxyServer.SetProgram(program)

		// Add TUI listener for request logs
		proxyServer.AddListener(func(log model.RequestLog) {
			program.Send(tui.CreateRequestMsg(log))
		})

		// Create a no-op logger to suppress all console output
		noopLogger := zap.NewNop()
		proxyServer.ReplaceLogger(noopLogger)

		// Set up servers in background
		var cleanup func() error
		var uiCleanup func() error

		// Start server setup in a goroutine
		go func() {
			// Wait for TUI to fully start before sending messages
			time.Sleep(1 * time.Second)
			
			// Send initial test message to Application Logs
			program.Send(tui.LogMsg{
				Level:   "INFO",
				Message: "🎨 TUI started successfully",
				Time:    time.Now(),
			})

			program.Send(tui.LogMsg{
				Level:   "INFO", 
				Message: "💡 Press 'q' or Ctrl+C to quit",
				Time:    time.Now(),
			})

			// Send log messages directly to TUI instead of using logger
			program.Send(tui.LogMsg{
				Level:   "INFO",
				Message: "🚀 Starting server setup...",
				Time:    time.Now(),
			})
			
			// Create a custom logger that only sends to TUI
			tuiOnlyLogger := &TUIOnlyLogger{program: program}
			
			if useLocalTailscale {
				// Use the TUI-only logger
				cleanup, uiCleanup = setupLocalTailscaleQuiet(ctx, tsClient, proxyServer, tuiOnlyLogger)
			} else {
				cleanup = setupTsnetQuiet(ctx, proxyServer, tuiOnlyLogger)
			}

			// Send completion message
			program.Send(tui.LogMsg{
				Level:   "INFO",
				Message: "✅ Server setup completed",
				Time:    time.Now(),
			})

			// Log web UI URL if available
			if !CLI.NoUI && proxyServer.GetWebUIURL() != "" {
				program.Send(tui.LogMsg{
					Level:   "INFO",
					Message: fmt.Sprintf("🌐 Web Dashboard: %s", proxyServer.GetWebUIURL()),
					Time:    time.Now(),
				})
			}
		}()

		// Run TUI and wait for shutdown - THIS SHOULD BE THE MAIN THREAD
		if _, err := program.Run(); err != nil {
			fmt.Printf("TUI error: %v\n", err)
		}

		// Cleanup after TUI exits - restore original logger for cleanup messages
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
}

func setupLocalTailscale(ctx context.Context, tsClient *tailscale.Client, proxyServer *proxy.Server, logger *zap.Logger, sugar *zap.SugaredLogger) (cleanup func() error, uiCleanup func() error) {
	// Find an available port for our local proxy server
	var proxyPort int
	var err error
	if CLI.Mock {
		proxyPort, err = tailscale.FindAvailableLocalPort(8080)
	} else {
		proxyPort, err = tailscale.FindAvailableLocalPort(CLI.Port + 1000)
	}
	if err != nil {
		sugar.Fatalf("Failed to find available port for proxy server: %v", err)
	}

	sugar.Infof("Starting local proxy server on port %d", proxyPort)

	// Start our proxy server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", proxyPort),
		Handler: proxyServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			sugar.Errorf("Proxy server error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Set up UI server if enabled
	if !CLI.NoUI {
		uiPort := CLI.UIPort
		if uiPort == 0 {
			uiPort, err = tailscale.FindAvailableLocalPort(9080)
			if err != nil {
				sugar.Warnf("Failed to find available port for UI server: %v", err)
				sugar.Infof("UI server disabled")
			}
		}

		if uiPort > 0 {
			sugar.Infof("Starting web UI server on port %d", uiPort)
			uiInfo, err := setupUIServer(ctx, tsClient, uiPort, proxyServer, sugar)
			if err != nil {
				sugar.Warnf("Failed to setup UI server: %v", err)
				sugar.Infof("Continuing without web UI")
			} else {
				sugar.Infof("🎨 Web UI dashboard: %s", uiInfo.URL)
				proxyServer.SetWebUIURL(uiInfo.URL)
				uiCleanup = func() error {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return uiInfo.Server.Shutdown(shutdownCtx)
				}
			}
		}
	}

	// Set up Tailscale serve
	sugar.Infof("Setting up Tailscale serve...")

	tsConfig := tailscale.Config{
		MountPath:    CLI.SetPath,
		EnableFunnel: CLI.Funnel,
		UseHTTPS:     CLI.UseHTTPS,
		ServePort:    CLI.ServePort,
		ProxyPort:    proxyPort,
	}

	err = tsClient.SetupServe(ctx, tsConfig)
	if err != nil {
		sugar.Fatalf("Failed to setup Tailscale serve: %v", err)
	}

	if CLI.Mock {
		sugar.Infof("🚀 tgate mock server configured with Tailscale serve")
		sugar.Infof("🔗 All requests will be logged and acknowledged")
	} else {
		sugar.Infof("🚀 tgate server configured with Tailscale serve + logging proxy")
		sugar.Infof("🔍 All requests will be logged and forwarded to localhost:%d", CLI.Port)
	}

	cleanup = func() error {
		// Cleanup Tailscale serve config
		tsClient.Cleanup(ctx, tsConfig)
		// Shutdown proxy server
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}

	return cleanup, uiCleanup
}

func setupTsnet(ctx context.Context, proxyServer *proxy.Server, logger *zap.Logger, sugar *zap.SugaredLogger) func() error {
	// Use tsnet mode
	tsnetConfig := tailscale.TSNetConfig{
		Hostname: CLI.TailscaleName,
		AuthKey:  CLI.AuthKey,
	}

	tsnetServer := tailscale.NewTSNetServer(tsnetConfig, sugar)

	go func() {
		if err := tsnetServer.Serve(ctx, proxyServer); err != nil {
			sugar.Errorf("TSNet server error: %v", err)
		}
	}()

	return func() error {
		return tsnetServer.Close()
	}
}

func setupUIServer(ctx context.Context, tsClient *tailscale.Client, uiPort int, proxyServer *proxy.Server, sugar *zap.SugaredLogger) (*model.UIServerInfo, error) {
	// Create UI server with the proxy server as the log provider
	uiServer := ui.NewServer(proxyServer, uiFiles)

	// Set up Tailscale serve for UI
	tailscalePort, uiURL, err := tsClient.SetupUIServe(ctx, uiPort)
	if err != nil {
		return nil, fmt.Errorf("failed to setup UI Tailscale serve: %w", err)
	}

	// Start UI server on local port
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", uiPort),
		Handler: uiServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			sugar.Errorf("UI server error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	sugar.Infof("🎨 Web UI server started on localhost:%d", uiPort)
	sugar.Infof("🎨 Web UI accessible at: %s", uiURL)

	return &model.UIServerInfo{
		Server:        httpServer,
		TailscalePort: tailscalePort,
		LocalPort:     uiPort,
		URL:           uiURL,
	}, nil
}

// TUIOnlyLogger sends all log messages to TUI instead of console
type TUIOnlyLogger struct {
	program *tea.Program
}

func (l *TUIOnlyLogger) Infof(format string, args ...interface{}) {
	l.program.Send(tui.LogMsg{
		Level:   "INFO",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Errorf(format string, args ...interface{}) {
	l.program.Send(tui.LogMsg{
		Level:   "ERROR",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Warnf(format string, args ...interface{}) {
	l.program.Send(tui.LogMsg{
		Level:   "WARN",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Debugf(format string, args ...interface{}) {
	l.program.Send(tui.LogMsg{
		Level:   "DEBUG",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

func (l *TUIOnlyLogger) Fatalf(format string, args ...interface{}) {
	l.program.Send(tui.LogMsg{
		Level:   "FATAL",
		Message: fmt.Sprintf(format, args...),
		Time:    time.Now(),
	})
}

// Simplified setup functions that only send messages to TUI
func setupLocalTailscaleQuiet(ctx context.Context, tsClient *tailscale.Client, proxyServer *proxy.Server, logger *TUIOnlyLogger) (cleanup func() error, uiCleanup func() error) {
	// Find an available port for our local proxy server
	var proxyPort int
	var err error
	if CLI.Mock {
		proxyPort, err = tailscale.FindAvailableLocalPort(8080)
	} else {
		proxyPort, err = tailscale.FindAvailableLocalPort(CLI.Port + 1000)
	}
	if err != nil {
		logger.Errorf("Failed to find available port for proxy server: %v", err)
		return nil, nil
	}

	logger.Infof("Starting local proxy server on port %d", proxyPort)

	// Start our proxy server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", proxyPort),
		Handler: proxyServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Proxy server error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Set up UI server if enabled
	if !CLI.NoUI {
		uiPort := CLI.UIPort
		if uiPort == 0 {
			uiPort, err = tailscale.FindAvailableLocalPort(9080)
			if err != nil {
				logger.Warnf("Failed to find available port for UI server: %v", err)
			}
		}

		if uiPort > 0 {
			logger.Infof("Starting web UI server on port %d", uiPort)
			uiInfo, err := setupUIServerQuiet(ctx, tsClient, uiPort, proxyServer, logger)
			if err != nil {
				logger.Warnf("Failed to setup UI server: %v", err)
			} else {
				logger.Infof("🎨 Web UI dashboard: %s", uiInfo.URL)
				proxyServer.SetWebUIURL(uiInfo.URL)
				uiCleanup = func() error {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return uiInfo.Server.Shutdown(shutdownCtx)
				}
			}
		}
	}

	// Set up Tailscale serve
	logger.Infof("Setting up Tailscale serve...")

	tsConfig := tailscale.Config{
		MountPath:    CLI.SetPath,
		EnableFunnel: CLI.Funnel,
		UseHTTPS:     CLI.UseHTTPS,
		ServePort:    CLI.ServePort,
		ProxyPort:    proxyPort,
	}

	err = tsClient.SetupServe(ctx, tsConfig)
	if err != nil {
		logger.Errorf("Failed to setup Tailscale serve: %v", err)
		return nil, nil
	}

	if CLI.Mock {
		logger.Infof("🚀 tgate mock server configured with Tailscale serve")
		logger.Infof("🔗 All requests will be logged and acknowledged")
	} else {
		logger.Infof("🚀 tgate server configured with Tailscale serve + logging proxy")
		logger.Infof("🔍 All requests will be logged and forwarded to localhost:%d", CLI.Port)
	}

	cleanup = func() error {
		// Cleanup Tailscale serve config
		tsClient.Cleanup(ctx, tsConfig)
		// Shutdown proxy server
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}

	return cleanup, uiCleanup
}

func setupTsnetQuiet(ctx context.Context, proxyServer *proxy.Server, logger *TUIOnlyLogger) func() error {
	logger.Infof("TSNet mode not implemented in quiet mode")
	return func() error { return nil }
}

func setupUIServerQuiet(ctx context.Context, tsClient *tailscale.Client, uiPort int, proxyServer *proxy.Server, logger *TUIOnlyLogger) (*model.UIServerInfo, error) {
	// Create UI server with the proxy server as the log provider
	uiServer := ui.NewServer(proxyServer, uiFiles)

	// Set up Tailscale serve for UI
	tailscalePort, uiURL, err := tsClient.SetupUIServe(ctx, uiPort)
	if err != nil {
		return nil, fmt.Errorf("failed to setup UI Tailscale serve: %w", err)
	}

	// Start UI server on local port
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", uiPort),
		Handler: uiServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("UI server error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	logger.Infof("🎨 Web UI server started on localhost:%d", uiPort)
	logger.Infof("🎨 Web UI accessible at: %s", uiURL)

	return &model.UIServerInfo{
		Server:        httpServer,
		TailscalePort: tailscalePort,
		LocalPort:     uiPort,
		URL:           uiURL,
	}, nil
}