package server

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/jaxxstorm/tgate/internal/config"
	"github.com/jaxxstorm/tgate/internal/httputil"
	"github.com/jaxxstorm/tgate/internal/model"
	"github.com/jaxxstorm/tgate/internal/proxy"
	"github.com/jaxxstorm/tgate/internal/tailscale"
	"github.com/jaxxstorm/tgate/internal/tui"
	"github.com/jaxxstorm/tgate/internal/ui"
)

// SetupLocalTailscaleQuiet sets up Tailscale serve with minimal TUI logging
func SetupLocalTailscaleQuiet(ctx context.Context, tsClient *tailscale.Client, proxyServer *proxy.Server, logger *tui.TUIOnlyLogger, cfg *config.Config, uiFiles fs.FS) (cleanup func() error, uiCleanup func() error) {
	// Find an available port for our local proxy server using random allocation
	proxyPort, err := tailscale.FindAvailableLocalPort()
	if err != nil {
		logger.Errorf("Port allocation failed: %v", err)
		return nil, nil
	}

	logger.Infof("Proxy starting port=%d", proxyPort)

	// Start our proxy server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", proxyPort),
		Handler: proxyServer,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Proxy server error port=%d error=%v", proxyPort, err)
		}
	}()

	// Wait for the server to be ready
	if err := httputil.WaitForServerReady(ctx, fmt.Sprintf("localhost:%d", proxyPort), 2*time.Second); err != nil {
		logger.Errorf("Proxy server failed to start port=%d error=%v", proxyPort, err)
		return nil, nil
	}

	// Set up UI server if enabled
	if !cfg.NoUI {
		uiPort := cfg.UIPort
		if uiPort == 0 {
			uiPort, err = tailscale.FindAvailableLocalPort()
			if err != nil {
				logger.Warnf("UI server port allocation failed error=%v fallback=disabled", err)
			}
		}

		if uiPort > 0 {
			logger.Infof("UI starting port=%d", uiPort)
			uiInfo, err := SetupUIServerQuiet(ctx, tsClient, uiPort, proxyServer, logger, uiFiles)
			if err != nil {
				logger.Warnf("UI setup failed port=%d", uiPort)
			} else {
				logger.Infof("UI operational port=%d", uiPort)
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
	logger.Infof("Tailscale serve starting port=%d", cfg.GetServePort())

	tsConfig := tailscale.Config{
		MountPath:    cfg.GetSetPath(),
		EnableFunnel: cfg.Funnel,
		UseHTTPS:     cfg.UseHTTPS,
		ServePort:    cfg.GetServePort(),
		ProxyPort:    proxyPort,
	}

	serviceInfo, err := tsClient.SetupServe(ctx, tsConfig)
	if err != nil {
		logger.Errorf("Tailscale serve setup failed config=%+v error=%v", tsConfig, err)
		return nil, nil
	}

	// Display service information
	if serviceInfo.IsFunnel {
		logger.Infof("Service ready url=%s access=internet", serviceInfo.URL)
	} else {
		logger.Infof("Service ready url=%s access=tailnet", serviceInfo.URL)
	}

	logger.Infof("Local testing url=%s", serviceInfo.LocalURL)

	if cfg.Mock {
		logger.Infof("Mock server operational port=%d", proxyPort)
	} else {
		logger.Infof("Proxy operational port=%d target=%d", proxyPort, cfg.Port)
	}

	cleanup = func() error {
		// Use a fresh context for cleanup to avoid cancellation issues
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		// Comprehensive cleanup of all Tailscale serve configs
		err := tsClient.CleanupAll(cleanupCtx)
		if err != nil {
			logger.Warnf("Failed to perform comprehensive cleanup, trying specific cleanup: %v", err)
			// Fallback to specific config cleanup
			tsClient.Cleanup(cleanupCtx, tsConfig)
		}
		// Shutdown proxy server
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}

	return cleanup, uiCleanup
}

// SetupTsnetQuiet sets up TSNet server with minimal TUI logging
func SetupTsnetQuiet(ctx context.Context, proxyServer *proxy.Server, logger *tui.TUIOnlyLogger, cfg *config.Config) func() error {
	logger.Infof("TSNet setup starting hostname=%s auth_key_provided=%t",
		cfg.TailscaleName, cfg.AuthKey != "")

	// For the quiet version, we'll need to create a zap logger from the TUIOnlyLogger
	tuiZapLogger := tui.CreateTUIOnlyZapLogger(logger)

	tsnetConfig := tailscale.TSNetConfig{
		Hostname: cfg.TailscaleName,
		AuthKey:  cfg.AuthKey,
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

// SetupUIServerQuiet sets up the UI server with minimal TUI logging
func SetupUIServerQuiet(ctx context.Context, tsClient *tailscale.Client, uiPort int, proxyServer *proxy.Server, logger *tui.TUIOnlyLogger, uiFiles fs.FS) (*model.UIServerInfo, error) {
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
			logger.Errorf("UI server runtime error local_port=%d error=%v", uiPort, err)
		}
	}()

	// Wait for the UI server to be ready
	if err := httputil.WaitForServerReady(ctx, fmt.Sprintf("localhost:%d", uiPort), 2*time.Second); err != nil {
		logger.Errorf("UI server failed to start local_port=%d error=%v", uiPort, err)
		return nil, fmt.Errorf("UI server failed to start: %w", err)
	}

	logger.Infof("UI bound port=%d", uiPort)
	logger.Infof("UI accessible url=%s", uiURL)

	return &model.UIServerInfo{
		Server:        httpServer,
		TailscalePort: tailscalePort,
		LocalPort:     uiPort,
		URL:           uiURL,
	}, nil
}
