// internal/tailscale/tsnet.go
package tailscale

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"go.uber.org/zap"
	"tailscale.com/tsnet"

	"github.com/jaxxstorm/tgate/internal/logging"
)

// TSNetConfig holds configuration for tsnet mode
type TSNetConfig struct {
	Hostname string
	AuthKey  string
}

// TSNetServer wraps a tsnet server with additional functionality
type TSNetServer struct {
	server *tsnet.Server
	logger *zap.Logger
	config TSNetConfig
}

// NewTSNetServer creates a new tsnet server with structured logging
func NewTSNetServer(config TSNetConfig, logger *zap.Logger) *TSNetServer {
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

	logger.Info("Creating TSNet server",
		logging.Component("tsnet_server"),
		logging.NodeName(config.Hostname),
		zap.Bool("has_auth_key", config.AuthKey != ""),
	)

	return &TSNetServer{
		server: server,
		logger: logger,
		config: config,
	}
}

// Listen creates a listener on the tsnet server
func (ts *TSNetServer) Listen(network, addr string) (net.Listener, error) {
	ts.logger.Info("Creating TSNet listener",
		logging.Component("tsnet_server"),
		logging.NodeName(ts.config.Hostname),
		zap.String("network", network),
		zap.String("address", addr),
	)

	ln, err := ts.server.Listen(network, addr)
	if err != nil {
		ts.logger.Error("Failed to create TSNet listener",
			logging.Component("tsnet_server"),
			zap.String("network", network),
			zap.String("address", addr),
			logging.Error(err),
		)
		return nil, fmt.Errorf("failed to listen on Tailscale device: %w", err)
	}

	ts.logger.Info("TSNet listener created successfully",
		logging.Component("tsnet_server"),
		zap.String("network", network),
		zap.String("address", addr),
	)

	return ln, nil
}

// Start starts the tsnet server and returns status information
func (ts *TSNetServer) Start(ctx context.Context) (string, error) {
	ts.logger.Info("Starting TSNet server",
		logging.Component("tsnet_server"),
		logging.NodeName(ts.config.Hostname),
	)

	// Get the device's Tailscale URL
	status, err := ts.server.Up(ctx)
	if err != nil {
		ts.logger.Error("Failed to start TSNet server",
			logging.Component("tsnet_server"),
			logging.NodeName(ts.config.Hostname),
			logging.Error(err),
		)
		return "", err
	}

	tailscaleURL := fmt.Sprintf("https://%s", status.Self.DNSName)

	ts.logger.Info("TSNet server started successfully",
		logging.Component("tsnet_server"),
		logging.NodeName(ts.config.Hostname),
		logging.URL(tailscaleURL),
		zap.String("dns_name", status.Self.DNSName),
	)

	return tailscaleURL, nil
}

// Serve starts serving HTTP on the tsnet server
func (ts *TSNetServer) Serve(ctx context.Context, handler http.Handler) error {
	ts.logger.Info("Setting up TSNet HTTP server",
		logging.Component("tsnet_server"),
		logging.NodeName(ts.config.Hostname),
	)

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

	ts.logger.Info("TSNet HTTP server ready to serve",
		logging.Component("tsnet_server"),
		logging.NodeName(ts.config.Hostname),
		logging.Port(80),
		logging.Status("serving"),
	)

	// Serve HTTP requests
	if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		ts.logger.Error("TSNet HTTP server error",
			logging.Component("tsnet_server"),
			logging.Error(err),
		)
		return fmt.Errorf("HTTP server error: %w", err)
	}

	return nil
}

// Close closes the tsnet server
func (ts *TSNetServer) Close() error {
	ts.logger.Info("Closing TSNet server",
		logging.Component("tsnet_server"),
		logging.NodeName(ts.config.Hostname),
	)

	err := ts.server.Close()
	if err != nil {
		ts.logger.Error("Error closing TSNet server",
			logging.Component("tsnet_server"),
			logging.Error(err),
		)
	} else {
		ts.logger.Info("TSNet server closed successfully",
			logging.Component("tsnet_server"),
		)
	}

	return err
}
