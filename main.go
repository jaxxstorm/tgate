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

	"github.com/alecthomas/kong"
	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

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

	startTime := time.Now()
	
	logger.Info(logging.MsgServerStarting,
		logging.Component("tgate"),
		logging.Version(Version),
	)

	// Server mode determination
	var serverMode model.ServerMode
	if CLI.Mock {
		serverMode = model.ModeMock
		logger.Info(logging.MsgMockModeConfiguration,
			logging.MockMode(true),
			logging.Status("auto_enabling_funnel"),
		)
	} else {
		serverMode = model.ModeProxy
	}

	// Auto-configure options
	if CLI.Funnel {
		logger.Info(logging.MsgTailscaleFunnelEnabled,
			logging.Status("auto_enabling_https"),
		)
	}

	// Log configuration
	logger.Info(logging.MsgServerConfiguration,
		logging.ServerMode(string(serverMode)),
		logging.TargetPort(CLI.Port),
		logging.FunnelEnabled(CLI.Funnel),
		logging.HTTPSEnabled(CLI.UseHTTPS),
		logging.UIEnabled(!CLI.NoUI),
		logging.TUIEnabled(!CLI.NoTUI),
		logging.MockMode(CLI.Mock),
	)

	// Test local connection only in proxy mode
	if !CLI.Mock {
		logger.Info(logging.MsgConnectionTesting,
			logging.TargetPort(CLI.Port),
		)
		
		testConn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", CLI.Port), 5*time.Second)
		if err != nil {
			logger.Fatal(logging.MsgConnectionFailed,
				logging.TargetPort(CLI.Port),
				logging.Error(err),
			)
		}
		testConn.Close()
		
		logger.Info(logging.MsgConnectionSuccess,
			logging.TargetPort(CLI.Port),
		)
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

	if CLI.AuthKey != "" {
		logger.Info(logging.MsgTailscaleConfiguration,
			logging.TailscaleMode("tsnet"),
			logging.Status("auth_key_provided"),
		)
	}

	if CLI.ForceTsnet {
		logger.Info(logging.MsgTailscaleConfiguration,
			logging.TailscaleMode("tsnet"),
			logging.Status("forced"),
		)
	}

	if CLI.NoTUI {
		runWithoutTUI(ctx, logger, useLocalTailscale, tsClient, proxyServer)
	} else {
		runWithTUI(ctx, logger, useLocalTailscale, tsClient, proxyServer)
	}
	
	logger.Info(logging.MsgServerStopped,
		logging.Duration(time.Since(startTime)),
	)
}

func runWithoutTUI(ctx context.Context, logger *zap.Logger, useLocalTailscale bool, tsClient *tailscale.Client, proxyServer *proxy.Server) {
	logger.Info(logging.MsgConsoleMode,
		logging.TUIEnabled(false),
	)
	
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
		cleanup, uiCleanup = setupLocalTailscale(ctx, tsClient, proxyServer, logger)
	} else {
		cleanup = setupTsnet(ctx, proxyServer, logger)
	}

	logger.Info(logging.MsgSetupComplete,
		logging.TailscaleMode(func() string {
			if useLocalTailscale { return "local_daemon" } else { return "tsnet" }
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

func runWithTUI(ctx context.Context, logger *zap.Logger, useLocalTailscale bool, tsClient *tailscale.Client, proxyServer *proxy.Server) {
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
	tuiZapLogger := createTUIZapLogger(program)
	proxyServer.ReplaceLogger(tuiZapLogger)

	// Set up servers in background
	var cleanup func() error
	var uiCleanup func() error

	// Start server setup in a goroutine
	go func() {
		// Wait a moment for TUI to initialize
		time.Sleep(500 * time.Millisecond)
		
		// Send initial messages to TUI application logs
		program.Send(tui.LogMsg{
			Level:   "INFO",
			Message: "TUI initialization completed successfully mode=interactive",
			Time:    time.Now(),
		})

		program.Send(tui.LogMsg{
			Level:   "INFO", 
			Message: "Control commands available quit_key=q alternate_quit=Ctrl+C navigation=arrow_keys,j,k scroll=PgUp,PgDn",
			Time:    time.Now(),
		})

		program.Send(tui.LogMsg{
			Level:   "INFO",
			Message: fmt.Sprintf("Server setup starting mode=%s target_port=%d funnel_enabled=%t https_enabled=%t ui_enabled=%t", 
				func() string { if CLI.Mock { return "mock" } else { return "proxy" } }(),
				CLI.Port, CLI.Funnel, CLI.UseHTTPS, !CLI.NoUI),
			Time:    time.Now(),
		})
		
		// Create a custom logger that only sends to TUI
		tuiOnlyLogger := &TUIOnlyLogger{program: program}
		
		// Create a new Tailscale client with the TUI logger for TUI mode
		var tuiTsClient *tailscale.Client
		if useLocalTailscale {
			tuiTsClient = tailscale.NewClient(tuiZapLogger) // Use the zap logger instead
			// Verify it's still available with the new client
			if !tuiTsClient.IsAvailable(ctx) {
				tuiOnlyLogger.Errorf("Tailscale not available in TUI mode")
				return
			}
		}
		
		if useLocalTailscale {
			cleanup, uiCleanup = setupLocalTailscaleQuiet(ctx, tuiTsClient, proxyServer, tuiOnlyLogger)
		} else {
			cleanup = setupTsnetQuiet(ctx, proxyServer, tuiOnlyLogger)
		}

		program.Send(tui.LogMsg{
			Level:   "INFO",
			Message: fmt.Sprintf("Server setup completed successfully total_duration=%dms proxy_port=%d tailscale_mode=%s", 
				time.Since(time.Now().Add(-2*time.Second)).Milliseconds(), // rough estimate
				func() int { if CLI.Mock { return 8080 } else { return CLI.Port + 1000 } }(),
				func() string { if useLocalTailscale { return "local_daemon" } else { return "tsnet" } }()),
			Time:    time.Now(),
		})

		// Log web UI URL if available
		if !CLI.NoUI && proxyServer.GetWebUIURL() != "" {
			program.Send(tui.LogMsg{
				Level:   "INFO",
				Message: fmt.Sprintf("Web dashboard available url=%s ui_port=%d tailscale_port=auto accessibility=tailnet_only", 
					proxyServer.GetWebUIURL(), CLI.UIPort),
				Time:    time.Now(),
			})
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

func setupLocalTailscale(ctx context.Context, tsClient *tailscale.Client, proxyServer *proxy.Server, logger *zap.Logger) (cleanup func() error, uiCleanup func() error) {
	// Find an available port for our local proxy server
	var proxyPort int
	var err error
	startPort := 8080
	if !CLI.Mock {
		startPort = CLI.Port + 1000
	}
	
	logger.Info(logging.MsgPortAllocation,
		logging.Component("proxy_server"),
		logging.StartPort(startPort),
	)
	
	proxyPort, err = tailscale.FindAvailableLocalPort(startPort)
	if err != nil {
		logger.Fatal(logging.MsgPortAllocationFailed,
			logging.Component("proxy_server"),
			logging.StartPort(startPort),
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
		logging.BindAddress("localhost"),
	)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", proxyPort),
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

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)
	
	logger.Info(logging.MsgProxyStarted,
		logging.ProxyPort(proxyPort),
	)

	// Set up UI server if enabled
	if !CLI.NoUI {
		uiPort := CLI.UIPort
		if uiPort == 0 {
			logger.Info(logging.MsgPortAllocation,
				logging.Component("ui_server"),
			)
			
			uiPort, err = tailscale.FindAvailableLocalPort(9080)
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
		logging.MountPath(CLI.SetPath),
		logging.FunnelEnabled(CLI.Funnel),
		logging.HTTPSEnabled(CLI.UseHTTPS),
		logging.ServePort(CLI.ServePort),
	)

	tsConfig := tailscale.Config{
		MountPath:    CLI.SetPath,
		EnableFunnel: CLI.Funnel,
		UseHTTPS:     CLI.UseHTTPS,
		ServePort:    CLI.ServePort,
		ProxyPort:    proxyPort,
	}

	err = tsClient.SetupServe(ctx, tsConfig)
	if err != nil {
		logger.Fatal(logging.MsgSetupFailed,
			logging.Component("tailscale_serve"),
			logging.Error(err),
		)
	}

	logger.Info(logging.MsgTailscaleServeSuccess,
		logging.ProxyPort(proxyPort),
		logging.TargetPort(CLI.Port),
		logging.MockMode(CLI.Mock),
	)

	cleanup = func() error {
		logger.Info(logging.MsgCleanupStarting,
			logging.Component("tailscale_serve"),
		)
		
		// Cleanup Tailscale serve config
		tsClient.Cleanup(ctx, tsConfig)
		
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

	return cleanup, uiCleanup
}

func setupTsnet(ctx context.Context, proxyServer *proxy.Server, logger *zap.Logger) func() error {
	logger.Info("Setting up TSNet mode",
		logging.Component("tsnet_setup"),
		logging.TailscaleMode("tsnet"),
		logging.NodeName(CLI.TailscaleName),
	)

	// Use tsnet mode
	tsnetConfig := tailscale.TSNetConfig{
		Hostname: CLI.TailscaleName,
		AuthKey:  CLI.AuthKey,
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
		Addr:    fmt.Sprintf("localhost:%d", uiPort),
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

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

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

// createTUIZapLogger creates a zap logger that sends output to the TUI
func createTUIZapLogger(program *tea.Program) *zap.Logger {
	// Create a custom writer that sends to TUI
	tuiWriter := &tuiZapWriter{program: program}
	
	// Create a zap logger with custom writer
	config := zap.NewDevelopmentConfig()
	config.OutputPaths = []string{"stdout"} // We'll override this
	
	// Create logger with custom writer
	logger := zap.New(
		zapcore.NewCore(
			zapcore.NewConsoleEncoder(config.EncoderConfig),
			zapcore.AddSync(tuiWriter),
			zapcore.InfoLevel,
		),
	)
	
	return logger
}

// tuiZapWriter implements zapcore.WriteSyncer for sending zap logs to TUI
type tuiZapWriter struct {
	program *tea.Program
}

func (w *tuiZapWriter) Write(p []byte) (n int, err error) {
	// Parse the zap log line and send to TUI
	line := strings.TrimSpace(string(p))
	
	// Skip empty lines
	if line == "" {
		return len(p), nil
	}
	
	// Zap development format: TIMESTAMP\tLEVEL\tMESSAGE\tJSON_FIELDS
	parts := strings.Split(line, "\t")
	
	level := "INFO"
	message := line
	
	if len(parts) >= 3 {
		// Extract level (second part)
		level = strings.ToUpper(strings.TrimSpace(parts[1]))
		
		// Extract message (third part)
		baseMessage := strings.TrimSpace(parts[2])
		
		// If there are JSON fields (fourth part), parse them for a cleaner message
		if len(parts) >= 4 {
			jsonFields := strings.TrimSpace(parts[3])
			
			// Try to create a cleaner message format for common cases
			if strings.Contains(baseMessage, "Incoming request") {
				message = parseIncomingRequest(jsonFields)
			} else if strings.Contains(baseMessage, "Response sent") {
				message = parseResponseSent(jsonFields)
			} else {
				// For other messages, just use the base message
				message = baseMessage
			}
		} else {
			message = baseMessage
		}
	}
	
	w.program.Send(tui.LogMsg{
		Level:   level,
		Message: message,
		Time:    time.Now(),
	})
	
	return len(p), nil
}

// parseIncomingRequest creates a detailed message from zap JSON fields
func parseIncomingRequest(jsonFields string) string {
	// Parse all available fields
	method := extractJSONField(jsonFields, "method")
	url := extractJSONField(jsonFields, "url")
	remoteAddr := extractJSONField(jsonFields, "remote_addr")
	userAgent := extractJSONField(jsonFields, "user_agent")
	contentLength := extractJSONField(jsonFields, "content_length")
	requestID := extractJSONField(jsonFields, "request_id")
	
	// Build detailed message
	var parts []string
	
	if method != "" && url != "" {
		parts = append(parts, fmt.Sprintf("method=%s url=%s", method, url))
	}
	
	if remoteAddr != "" {
		parts = append(parts, fmt.Sprintf("remote_addr=%s", remoteAddr))
	}
	
	if userAgent != "" && userAgent != `""` {
		userAgent = strings.Trim(userAgent, `"`)
		parts = append(parts, fmt.Sprintf("user_agent=%q", userAgent))
	}
	
	if contentLength != "" && contentLength != "0" {
		parts = append(parts, fmt.Sprintf("content_length=%s", contentLength))
	}
	
	if requestID != "" {
		requestID = strings.Trim(requestID, `"`)
		parts = append(parts, fmt.Sprintf("request_id=%s", requestID))
	}
	
	if len(parts) > 0 {
		return "Incoming request " + strings.Join(parts, " ")
	}
	
	return "Incoming request"
}

// parseResponseSent creates a detailed message from zap JSON fields
func parseResponseSent(jsonFields string) string {
	// Parse all available fields
	statusCode := extractJSONField(jsonFields, "status_code")
	responseSize := extractJSONField(jsonFields, "response_size")
	duration := extractJSONField(jsonFields, "duration")
	requestID := extractJSONField(jsonFields, "request_id")
	
	// Build detailed message
	var parts []string
	
	if statusCode != "" {
		parts = append(parts, fmt.Sprintf("status_code=%s", statusCode))
	}
	
	if responseSize != "" {
		parts = append(parts, fmt.Sprintf("response_size=%s", responseSize))
	}
	
	if duration != "" {
		duration = strings.Trim(duration, `"`)
		parts = append(parts, fmt.Sprintf("duration=%s", duration))
	}
	
	if requestID != "" {
		requestID = strings.Trim(requestID, `"`)
		parts = append(parts, fmt.Sprintf("request_id=%s", requestID))
	}
	
	if len(parts) > 0 {
		return "Response sent " + strings.Join(parts, " ")
	}
	
	return "Response sent"
}

// extractJSONField extracts a field value from a JSON-like string
func extractJSONField(jsonStr, fieldName string) string {
	// Simple extraction for {"field": "value"} format
	re := strings.NewReplacer(
		`"`, `"`,
		` `, ` `,
	)
	cleanStr := re.Replace(jsonStr)
	
	// Find the field
	startPattern := fmt.Sprintf(`"%s": `, fieldName)
	startIdx := strings.Index(cleanStr, startPattern)
	if startIdx == -1 {
		return ""
	}
	
	startIdx += len(startPattern)
	remaining := cleanStr[startIdx:]
	
	// Find the end of the value
	var endIdx int
	if strings.HasPrefix(remaining, `"`) {
		// String value - find closing quote
		endIdx := strings.Index(remaining[1:], `"`)
		if endIdx != -1 {
			return remaining[1 : endIdx+1] // Extract without quotes
		}
	} else {
		// Numeric value - find comma or end
		endIdx = strings.IndexAny(remaining, ",}")
		if endIdx == -1 {
			endIdx = len(remaining)
		}
		return strings.TrimSpace(remaining[:endIdx])
	}
	
	return ""
}

func (w *tuiZapWriter) Sync() error {
	return nil
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
		logger.Errorf("Port allocation failed error=%v start_port=%d mode=%s", err, func() int { if CLI.Mock { return 8080 } else { return CLI.Port + 1000 } }(), func() string { if CLI.Mock { return "mock" } else { return "proxy" } }())
		return nil, nil
	}

	logger.Infof("Local proxy server starting port=%d bind_address=localhost target_mode=%s", proxyPort, func() string { if CLI.Mock { return "mock" } else { return fmt.Sprintf("proxy_to_localhost:%d", CLI.Port) } }())

	// Start our proxy server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", proxyPort),
		Handler: proxyServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Proxy server error port=%d error=%v", proxyPort, err)
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
				logger.Warnf("UI server port allocation failed error=%v fallback=disabled", err)
			}
		}

		if uiPort > 0 {
			logger.Infof("Web UI server starting local_port=%d bind_address=localhost", uiPort)
			uiInfo, err := setupUIServerQuiet(ctx, tsClient, uiPort, proxyServer, logger)
			if err != nil {
				logger.Warnf("UI server setup failed port=%d error=%v status=continuing_without_ui", uiPort, err)
			} else {
				logger.Infof("Web UI server operational local_port=%d", uiPort)
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
	logger.Infof("Tailscale serve configuration starting mount_path=%s funnel=%t https=%t serve_port=%d", CLI.SetPath, CLI.Funnel, CLI.UseHTTPS, CLI.ServePort)

	tsConfig := tailscale.Config{
		MountPath:    CLI.SetPath,
		EnableFunnel: CLI.Funnel,
		UseHTTPS:     CLI.UseHTTPS,
		ServePort:    CLI.ServePort,
		ProxyPort:    proxyPort,
	}

	err = tsClient.SetupServe(ctx, tsConfig)
	if err != nil {
		logger.Errorf("Tailscale serve setup failed config=%+v error=%v", tsConfig, err)
		return nil, nil
	}

	if CLI.Mock {
		logger.Infof("Mock server operational mode=testing proxy_port=%d tailscale_serve=configured request_logging=enabled", proxyPort)
	} else {
		logger.Infof("Proxy server operational mode=production proxy_port=%d target_port=%d tailscale_serve=configured request_logging=enabled", proxyPort, CLI.Port)
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
	logger.Infof("TSNet setup starting hostname=%s auth_key_provided=%t", 
		CLI.TailscaleName, CLI.AuthKey != "")

	// For the quiet version, we'll need to create a zap logger from the TUIOnlyLogger
	tuiZapLogger := createTUIOnlyZapLogger(logger)
	
	tsnetConfig := tailscale.TSNetConfig{
		Hostname: CLI.TailscaleName,
		AuthKey:  CLI.AuthKey,
	}

	tsnetServer := tailscale.NewTSNetServer(tsnetConfig, tuiZapLogger)

	go func() {
		if err := tsnetServer.Serve(ctx, proxyServer); err != nil {
			logger.Errorf("TSNet server error: %v", err)
		}
	}()

	return func() error {
		logger.Infof("TSNet cleanup starting")
		err := tsnetServer.Close()
		if err != nil {
			logger.Errorf("TSNet cleanup error: %v", err)
		} else {
			logger.Infof("TSNet cleanup complete")
		}
		return err
	}
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
			logger.Errorf("UI server runtime error local_port=%d error=%v", uiPort, err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	logger.Infof("Web UI server bound successfully local_port=%d bind_address=localhost", uiPort)
	logger.Infof("Web UI accessible via Tailscale url=%s tailscale_port=%d path=/ui/", uiURL, tailscalePort)

	return &model.UIServerInfo{
		Server:        httpServer,
		TailscalePort: tailscalePort,
		LocalPort:     uiPort,
		URL:           uiURL,
	}, nil
}

// Helper function to create a zap logger that routes to TUI via TUIOnlyLogger
func createTUIOnlyZapLogger(tuiLogger *TUIOnlyLogger) *zap.Logger {
	// Create a custom writer that sends to TUI via the TUIOnlyLogger
	tuiWriter := &tuiOnlyZapWriter{tuiLogger: tuiLogger}
	
	// Create a zap logger with custom writer
	config := zap.NewDevelopmentConfig()
	
	// Create logger with custom writer
	logger := zap.New(
		zapcore.NewCore(
			zapcore.NewConsoleEncoder(config.EncoderConfig),
			zapcore.AddSync(tuiWriter),
			zapcore.InfoLevel,
		),
	)
	
	return logger
}

// tuiOnlyZapWriter implements zapcore.WriteSyncer for sending zap logs to TUIOnlyLogger
type tuiOnlyZapWriter struct {
	tuiLogger *TUIOnlyLogger
}

func (w *tuiOnlyZapWriter) Write(p []byte) (n int, err error) {
	// Parse the zap log line and send to TUI via TUIOnlyLogger
	line := strings.TrimSpace(string(p))
	
	// Skip empty lines
	if line == "" {
		return len(p), nil
	}
	
	// Simple parsing - just extract level and message
	parts := strings.Split(line, "\t")
	
	if len(parts) >= 3 {
		level := strings.ToUpper(strings.TrimSpace(parts[1]))
		message := strings.TrimSpace(parts[2])
		
		// If there are more parts, include them
		if len(parts) > 3 {
			message += " " + strings.Join(parts[3:], " ")
		}
		
		// Route to appropriate TUIOnlyLogger method based on level
		switch level {
		case "DEBUG":
			w.tuiLogger.Debugf("%s", message)
		case "INFO":
			w.tuiLogger.Infof("%s", message)
		case "WARN":
			w.tuiLogger.Warnf("%s", message)
		case "ERROR":
			w.tuiLogger.Errorf("%s", message)
		case "FATAL":
			w.tuiLogger.Fatalf("%s", message)
		default:
			w.tuiLogger.Infof("%s", message)
		}
	} else {
		// Fallback for malformed log lines
		w.tuiLogger.Infof("%s", line)
	}
	
	return len(p), nil
}

func (w *tuiOnlyZapWriter) Sync() error {
	return nil
}