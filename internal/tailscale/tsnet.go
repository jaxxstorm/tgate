package tailscale

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"go.uber.org/zap"
	"tailscale.com/tsnet"
)

// TSNetConfig holds configuration for tsnet mode
type TSNetConfig struct {
	Hostname string
	AuthKey  string
}

// TSNetServer wraps a tsnet server with additional functionality
type TSNetServer struct {
	server  *tsnet.Server
	logger  *zap.SugaredLogger
	config  TSNetConfig
}

// NewTSNetServer creates a new tsnet server
func NewTSNetServer(config TSNetConfig, logger *zap.SugaredLogger) *TSNetServer {
	var server *tsnet.Server
	if config.AuthKey != "" {
		server = &tsnet.Server{
			Hostname: config.Hostname,
			AuthKey:  config.AuthKey,
		}
	} else {
		server = &tsnet.Server{
			Hostname: config.Hostname,
		}
	}

	return &TSNetServer{
		server: server,
		logger: logger,
		config: config,
	}
}

// Listen creates a listener on the tsnet server
func (ts *TSNetServer) Listen(network, addr string) (net.Listener, error) {
	ts.logger.Infof("Tailscale node name: %s", ts.config.Hostname)
	
	ln, err := ts.server.Listen(network, addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on Tailscale device: %w", err)
	}

	return ln, nil
}

// Start starts the tsnet server and returns status information
func (ts *TSNetServer) Start(ctx context.Context) (string, error) {
	// Get the device's Tailscale URL
	status, err := ts.server.Up(ctx)
	if err != nil {
		ts.logger.Warnf("Could not get device status: %v", err)
		return "", err
	}

	tailscaleURL := fmt.Sprintf("https://%s", status.Self.DNSName)
	ts.logger.Infof("📡 Tailscale URL: %s", tailscaleURL)
	
	return tailscaleURL, nil
}

// Serve starts serving HTTP on the tsnet server
func (ts *TSNetServer) Serve(ctx context.Context, handler http.Handler) error {
	ln, err := ts.Listen("tcp", ":80")
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler: handler,
	}

	// Start the device
	_, err = ts.Start(ctx)
	if err != nil {
		return err
	}

	ts.logger.Infof("🚀 tgate server started with tsnet")
	
	// Serve HTTP requests
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}

	return nil
}

// Close closes the tsnet server
func (ts *TSNetServer) Close() error {
	return ts.server.Close()
}