package tailscale

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"go.uber.org/zap"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
)

// Config holds configuration for Tailscale setup
type Config struct {
	MountPath   string
	EnableFunnel bool
	UseHTTPS    bool
	ServePort   int
	ProxyPort   int
}

// Client wraps the Tailscale local client with additional functionality
type Client struct {
	lc     *local.Client
	logger *zap.SugaredLogger
}

// NewClient creates a new Tailscale client
func NewClient(logger *zap.SugaredLogger) *Client {
	return &Client{
		lc:     &local.Client{},
		logger: logger,
	}
}

// IsAvailable checks if local Tailscale is available
func (c *Client) IsAvailable(ctx context.Context) bool {
	_, err := c.lc.Status(ctx)
	return err == nil
}

// GetDNSName returns the DNS name of this Tailscale node
func (c *Client) GetDNSName(ctx context.Context) (string, error) {
	status, err := c.lc.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get status: %w", err)
	}
	return strings.TrimSuffix(status.Self.DNSName, "."), nil
}

// SetupServe configures Tailscale serve for the given configuration
func (c *Client) SetupServe(ctx context.Context, config Config) error {
	// Get current serve config
	sc, err := c.lc.GetServeConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get serve config: %w", err)
	}
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	// Get DNS name
	dnsName, err := c.GetDNSName(ctx)
	if err != nil {
		return err
	}

	// Set up HTTP handler for the proxy target
	h := &ipn.HTTPHandler{
		Proxy: fmt.Sprintf("http://localhost:%d", config.ProxyPort),
	}

	// Clean mount path
	mountPath := config.MountPath
	if mountPath == "" {
		mountPath = "/"
	}
	if !strings.HasPrefix(mountPath, "/") {
		mountPath = "/" + mountPath
	}

	// Determine serve port and TLS usage
	var srvPort uint16
	var useTLS bool

	if config.ServePort != 0 {
		srvPort = uint16(config.ServePort)
		useTLS = config.UseHTTPS || config.ServePort == 443
	} else {
		if config.UseHTTPS {
			srvPort = 443
			useTLS = true
		} else {
			srvPort = 80
			useTLS = false
		}
	}

	c.logger.Infof("Setting up Tailscale serve on port %d (TLS: %t)", srvPort, useTLS)

	// Check if port is already in use
	if sc.IsTCPForwardingOnPort(srvPort) {
		return fmt.Errorf("port %d is already serving TCP", srvPort)
	}

	// Set web handler
	sc.SetWebHandler(h, dnsName, srvPort, mountPath, useTLS)

	// If using HTTPS/TLS, set up TCP handler for TLS termination
	if useTLS {
		c.logger.Infof("🔍 Setting up HTTPS TCP handler for TLS termination...")
		if sc.TCP == nil {
			sc.TCP = make(map[uint16]*ipn.TCPPortHandler)
		}
		sc.TCP[srvPort] = &ipn.TCPPortHandler{
			HTTPS: true,
		}

		if err := c.enableHTTPSFeature(ctx); err != nil {
			c.logger.Warnf("⚠️  HTTPS feature check failed: %v", err)
			c.logger.Infof("💡 HTTPS may not work properly without certificates")
			c.logger.Infof("💡 Consider using HTTP mode instead: remove --use-https flag")
		}
	}

	// Enable funnel if requested (only works with HTTPS/443)
	if config.EnableFunnel {
		if !useTLS || srvPort != 443 {
			c.logger.Warnf("Funnel requires HTTPS on port 443, but serving on port %d with TLS=%t", srvPort, useTLS)
			c.logger.Infof("Consider using --use-https or --serve-port=443")
			return fmt.Errorf("funnel requires HTTPS on port 443")
		}

		// Enable HTTPS feature first if needed
		if err := c.enableHTTPSFeature(ctx); err != nil {
			c.logger.Errorf("❌ Failed to enable HTTPS feature: %v", err)
			c.logger.Infof("💡 Please enable HTTPS certificates in your Tailscale admin console:")
			c.logger.Infof("   1. Go to https://login.tailscale.com/admin/dns")
			c.logger.Infof("   2. Enable 'HTTPS Certificates'")
			c.logger.Infof("   3. Wait a few minutes for provisioning")
			c.logger.Infof("   4. Try again")
			return fmt.Errorf("HTTPS certificates not enabled: %w", err)
		}

		// Check certificate status before enabling funnel
		c.logger.Infof("🔍 Checking HTTPS certificate status for funnel...")
		if err := c.checkTailscaleCertificates(ctx, dnsName); err != nil {
			c.logger.Errorf("❌ Certificate check failed: %v", err)
			c.logger.Infof("💡 You can:")
			c.logger.Infof("   • Wait a few minutes if certificates are still provisioning")
			c.logger.Infof("   • Try running without --funnel first to test local access")
			c.logger.Infof("   • Check https://login.tailscale.com/admin/dns for certificate settings")
			return fmt.Errorf("cannot enable funnel: %w", err)
		}

		sc.SetFunnel(dnsName, srvPort, true)
		c.logger.Infof("🌍 Funnel enabled - service will be available on the internet")
	}

	// Apply the serve config
	err = c.lc.SetServeConfig(ctx, sc)
	if err != nil {
		return fmt.Errorf("failed to set serve config: %w", err)
	}

	// Display URL information
	scheme := "http"
	if useTLS {
		scheme = "https"
	}

	portPart := ""
	if (scheme == "http" && srvPort != 80) || (scheme == "https" && srvPort != 443) {
		portPart = fmt.Sprintf(":%d", srvPort)
	}

	url := fmt.Sprintf("%s://%s%s%s", scheme, dnsName, portPart, mountPath)

	if config.EnableFunnel {
		c.logger.Infof("🌍 Available on the internet: %s", url)
		c.logger.Infof("💡 If you get TLS errors, certificates may still be provisioning")
		c.logger.Infof("💡 Try again in 2-3 minutes if the connection fails")
	} else {
		c.logger.Infof("🔒 Available within your tailnet: %s", url)
		if useTLS {
			c.logger.Infof("💡 If you get TLS errors, try HTTP first or wait for certificate provisioning")
		}
	}

	return nil
}

// SetupUIServe sets up Tailscale serve for the UI dashboard
func (c *Client) SetupUIServe(ctx context.Context, uiPort int) (uint16, string, error) {
	// Get current serve config
	sc, err := c.lc.GetServeConfig(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get serve config: %w", err)
	}
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	// Get DNS name
	dnsName, err := c.GetDNSName(ctx)
	if err != nil {
		return 0, "", err
	}

	// Find available Tailscale port starting from a random port
	tailscalePort, err := c.findAvailableTailscalePort(sc, 8080)
	if err != nil {
		return 0, "", fmt.Errorf("failed to find available Tailscale port: %w", err)
	}

	uiHandler := &ipn.HTTPHandler{
		Proxy: fmt.Sprintf("http://localhost:%d", uiPort),
	}

	sc.SetWebHandler(uiHandler, dnsName, tailscalePort, "/ui/", false) // HTTP only, no TLS

	// Apply the serve config
	err = c.lc.SetServeConfig(ctx, sc)
	if err != nil {
		return 0, "", fmt.Errorf("failed to set UI serve config: %w", err)
	}

	uiURL := fmt.Sprintf("http://%s:%d/ui/", dnsName, tailscalePort)
	c.logger.Infof("🎨 Web UI available within your tailnet: %s", uiURL)

	return tailscalePort, uiURL, nil
}

// Cleanup removes Tailscale serve configuration
func (c *Client) Cleanup(ctx context.Context, config Config) error {
	sc, err := c.lc.GetServeConfig(ctx)
	if err != nil || sc == nil {
		c.logger.Debugf("No serve config to clean up: %v", err)
		return nil // Nothing to clean up
	}

	dnsName, err := c.GetDNSName(ctx)
	if err != nil {
		c.logger.Warnf("Failed to get DNS name during cleanup: %v", err)
		return err
	}

	mountPath := config.MountPath
	if mountPath == "" {
		mountPath = "/"
	}
	if !strings.HasPrefix(mountPath, "/") {
		mountPath = "/" + mountPath
	}

	// Determine the port we used for serving
	var srvPort uint16
	if config.ServePort != 0 {
		srvPort = uint16(config.ServePort)
	} else {
		if config.UseHTTPS {
			srvPort = 443
		} else {
			srvPort = 80
		}
	}

	// Helper function to safely remove web handlers
	safeRemoveWebHandler := func(dnsName string, port uint16, paths []string, allowFunnel bool) {
		defer func() {
			if r := recover(); r != nil {
				c.logger.Warnf("Recovered from panic while removing web handler on port %d: %v", port, r)
			}
		}()

		// Check if the web config exists for this port before trying to remove
		if sc.Web != nil {
			hostPort := ipn.HostPort(dnsName + ":" + fmt.Sprintf("%d", port))
			if hostConfig, exists := sc.Web[hostPort]; exists {
				if _, portExists := hostConfig.Handlers[fmt.Sprintf("%d", port)]; portExists {
					sc.RemoveWebHandler(dnsName, port, paths, allowFunnel)
					c.logger.Debugf("Removed web handler for %s:%d%v", dnsName, port, paths)
				}
			}
		}
	}

	// Remove main service handler
	safeRemoveWebHandler(dnsName, srvPort, []string{mountPath}, true)

	// Also cleanup potential UI handlers
	safeRemoveWebHandler(dnsName, 8080, []string{"/ui/"}, false)
	safeRemoveWebHandler(dnsName, 8081, []string{"/ui/"}, false)
	safeRemoveWebHandler(dnsName, 8082, []string{"/ui/"}, false)

	// Apply the updated config
	err = c.lc.SetServeConfig(ctx, sc)
	if err != nil {
		c.logger.Warnf("Failed to cleanup serve config: %v", err)
		return err
	} else {
		c.logger.Infof("Cleaned up Tailscale serve configuration")
	}

	return nil
}

// enableHTTPSFeature enables HTTPS capability for the tailnet
func (c *Client) enableHTTPSFeature(ctx context.Context) error {
	// Check if HTTPS is already enabled
	status, err := c.lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	if status.Self.HasCap(tailcfg.CapabilityHTTPS) {
		c.logger.Infof("✅ HTTPS capability already enabled")
		return nil
	}

	c.logger.Infof("🔍 HTTPS capability not enabled, need to enable it...")
	c.logger.Infof("💡 This will enable HTTPS certificate provisioning for your tailnet")
	c.logger.Infof("💡 Go to https://login.tailscale.com/admin/dns and enable 'HTTPS Certificates'")
	c.logger.Infof("💡 Or wait while we try to enable it automatically...")

	// Try to enable HTTPS capability
	// Note: This might require admin permissions or interactive approval
	// The exact API for this isn't publicly documented, so we'll provide guidance

	return fmt.Errorf("HTTPS capability needs to be enabled in your Tailscale admin console")
}

// checkTailscaleCertificates checks if HTTPS certificates are available
func (c *Client) checkTailscaleCertificates(ctx context.Context, dnsName string) error {
	// Check if HTTPS certificates are available
	status, err := c.lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	// Check if the node has HTTPS capability
	if !status.Self.HasCap(tailcfg.CapabilityHTTPS) {
		c.logger.Warnf("❌ Node does not have HTTPS capability enabled")
		c.logger.Infof("💡 To enable HTTPS certificates:")
		c.logger.Infof("   1. Go to https://login.tailscale.com/admin/dns")
		c.logger.Infof("   2. Enable 'HTTPS Certificates' for your tailnet")
		c.logger.Infof("   3. Wait a few minutes for certificate provisioning")
		return fmt.Errorf("HTTPS certificates not enabled for this tailnet")
	}

	c.logger.Infof("✅ HTTPS capability is enabled for this tailnet")

	// Check certificate status - this is a bit tricky as the API doesn't directly expose cert status
	// We can try to check if certificates exist by looking at the certificate domains
	if len(status.CertDomains) == 0 {
		c.logger.Warnf("⚠️  No certificate domains found")
		c.logger.Infof("💡 Certificate provisioning may still be in progress")
		return fmt.Errorf("no certificate domains available")
	}

	// Check if our domain is in the cert domains
	found := false
	for _, domain := range status.CertDomains {
		if strings.Contains(domain, strings.Split(dnsName, ".")[0]) {
			found = true
			break
		}
	}

	if !found {
		c.logger.Warnf("⚠️  Certificate not found for domain %s", dnsName)
		c.logger.Infof("💡 Available certificate domains: %v", status.CertDomains)
		c.logger.Infof("💡 Certificate provisioning may still be in progress")
		return fmt.Errorf("certificate not available for domain %s", dnsName)
	}

	c.logger.Infof("✅ Certificate appears to be available for %s", dnsName)
	return nil
}

// findAvailableTailscalePort finds an available port for Tailscale serve
func (c *Client) findAvailableTailscalePort(sc *ipn.ServeConfig, startPort uint16) (uint16, error) {
	// Use a more random starting port to avoid conflicts
	rand.Seed(time.Now().UnixNano())
	randomOffset := rand.Intn(100) // Random offset 0-99
	actualStartPort := startPort + uint16(randomOffset)
	
	for port := actualStartPort; port < actualStartPort+200; port++ {
		if !sc.IsTCPForwardingOnPort(port) {
			// Also check if web handlers exist on this port
			available := true
			if sc.Web != nil {
				for hostPort := range sc.Web {
					if strings.Contains(string(hostPort), fmt.Sprintf(":%d", port)) {
						available = false
						break
					}
				}
			}
			if available {
				return port, nil
			}
		}
	}
	return 0, fmt.Errorf("no available Tailscale port found starting from %d", actualStartPort)
}

// FindAvailableLocalPort finds an available local port
func FindAvailableLocalPort(startPort int) (int, error) {
	for port := startPort; port < startPort+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found starting from %d", startPort)
}