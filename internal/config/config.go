package config

import (
	"fmt"

	"github.com/alecthomas/kong"
)

// CLI represents the command line interface configuration
type CLI struct {
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
	CleanupServe  bool   `kong:"help='Clear all Tailscale serve configurations and exit'"`
}

// Config holds the parsed and validated configuration
type Config struct {
	CLI
}

// Parse parses command line arguments and returns a validated configuration
func Parse() (*Config, error) {
	var cli CLI
	kong.Parse(&cli)

	// Handle version flag
	if cli.Version {
		return &Config{CLI: cli}, nil
	}

	// Handle cleanup flag
	if cli.CleanupServe {
		return &Config{CLI: cli}, nil
	}

	// Validate arguments
	if cli.Mock && cli.Port != 0 {
		return nil, fmt.Errorf("cannot specify both port and --mock flag\nUsage: tgate <port> [flags]     (proxy mode)\n       tgate --mock [flags]     (mock/testing mode)\n       tgate --version\n       tgate --cleanup-serve")
	}

	if !cli.Mock && cli.Port == 0 {
		return nil, fmt.Errorf("port argument is required (or use --mock for testing mode)\nUsage: tgate <port> [flags]     (proxy mode)\n       tgate --mock [flags]     (mock/testing mode)\n       tgate --version\n       tgate --cleanup-serve")
	}

	// Auto-configure options
	config := &Config{CLI: cli}
	config.applyAutoConfiguration()

	return config, nil
}

// applyAutoConfiguration applies automatic configuration rules
func (c *Config) applyAutoConfiguration() {
	// Auto-enable funnel for mock mode unless explicitly disabled
	if c.Mock && !c.Funnel {
		c.Funnel = true
		c.UseHTTPS = true
	}

	// If funnel is enabled, automatically enable HTTPS since funnel requires it
	if c.Funnel {
		c.UseHTTPS = true
	}
}

// GetSetPath returns the mount path with default fallback
func (c *Config) GetSetPath() string {
	if c.SetPath == "" {
		return "/"
	}
	return c.SetPath
}

// GetServePort returns the serve port with protocol-based defaults
func (c *Config) GetServePort() int {
	if c.ServePort == 0 {
		if c.UseHTTPS {
			return 443
		}
		return 80
	}
	return c.ServePort
}
