// internal/logging/messages.go
package logging

// Standard log messages for consistency
const (
	// Startup messages
	MsgServerStarting = "Server starting"
	MsgServerStarted  = "Server started successfully"
	MsgServerStopping = "Server stopping"
	MsgServerStopped  = "Server stopped"
	MsgServerShutdown = "Server shutdown complete"

	// Configuration messages
	MsgConfigLoaded          = "Configuration loaded"
	MsgConfigValidated       = "Configuration validated"
	MsgServerConfiguration   = "Server configuration"
	MsgMockModeConfiguration = "Mock mode configuration"

	// Connection messages
	MsgConnectionTesting = "Testing connection"
	MsgConnectionSuccess = "Connection successful"
	MsgConnectionFailed  = "Connection failed"

	// Tailscale messages
	MsgTailscaleDetected      = "Tailscale daemon detected"
	MsgTailscaleNotAvailable  = "Tailscale daemon not available"
	MsgTailscaleServeSetup    = "Tailscale serve configuration"
	MsgTailscaleServeSuccess  = "Tailscale serve configured successfully"
	MsgTailscaleFunnelEnabled = "Tailscale funnel enabled"
	MsgTailscaleConfiguration = "Tailscale configuration"
	MsgTailscaleAvailability  = "Checking Tailscale availability"
	MsgTailscaleDaemonReady   = "Tailscale daemon available"
	MsgTailscaleDaemonMissing = "Tailscale daemon not available"

	// Proxy messages
	MsgProxyStarting   = "Proxy server starting"
	MsgProxyStarted    = "Proxy server started"
	MsgProxyConfigured = "Proxy server configured"

	// UI messages
	MsgUIStarting  = "Web UI server starting"
	MsgUIStarted   = "Web UI server started"
	MsgUIAvailable = "Web UI available"
	MsgUIDisabled  = "Web UI disabled"

	// TUI messages
	MsgTUIStarting = "TUI initializing"
	MsgTUIStarted  = "TUI started successfully"
	MsgTUIDisabled = "TUI disabled"
	MsgConsoleMode = "Starting console mode"

	// Port allocation
	MsgPortAllocation       = "Port allocation"
	MsgPortAllocated        = "Port allocated successfully"
	MsgPortAllocationFailed = "Port allocation failed"

	// Setup and cleanup
	MsgSetupStarting   = "Setup starting"
	MsgSetupComplete   = "Setup complete"
	MsgCleanupStarting = "Cleanup starting"
	MsgCleanupComplete = "Cleanup complete"

	// Error messages
	MsgSetupFailed        = "Setup failed"
	MsgStartupFailed      = "Startup failed"
	MsgConfigurationError = "Configuration error"
	MsgRuntimeError       = "Runtime error"

	// Request/Response messages
	MsgIncomingRequest  = "Incoming request"
	MsgResponseSent     = "Response sent"
	MsgRequestProcessed = "Request processed"

	// Service availability messages
	MsgServiceReady      = "Service is ready and accessible"
	MsgServiceURLs       = "Service URLs"
	MsgTailnetAccess     = "Service accessible via Tailnet"
	MsgInternetAccess    = "Service accessible via Internet (Funnel)"
	MsgLocalAccess       = "Service accessible locally"
	MsgAccessibilityInfo = "Service accessibility"
)
