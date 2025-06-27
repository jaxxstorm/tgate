package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

var CLI struct {
	Port          int    `kong:"arg,required,help='Local port to expose'"`
	TailscaleName string `kong:"short='n',default='tgate',help='Tailscale node name (only used with tsnet mode)'"`
	Funnel        bool   `kong:"short='f',help='Enable Tailscale funnel (public internet access)'"`
	Verbose       bool   `kong:"short='v',help='Enable verbose logging'"`
	JSON          bool   `kong:"short='j',help='Output logs in JSON format'"`
	WebUI         bool   `kong:"short='w',help='Enable web UI (future feature)'"`
	LogFile       string `kong:"help='Log file path (optional)'"`
	AuthKey       string `kong:"help='Tailscale auth key to create separate tsnet device'"`
	ForceTsnet    bool   `kong:"help='Force tsnet mode even if local Tailscale is available'"`
	SetPath       string `kong:"help='Set custom path for serve (default: /)'"`
	ServePort     int    `kong:"help='Tailscale serve port (default: 80 for HTTP, 443 for HTTPS)'"`
	UseHTTPS      bool   `kong:"help='Use HTTPS instead of HTTP for Tailscale serve'"`
}

type RequestLog struct {
	Timestamp   time.Time         `json:"timestamp"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	RemoteAddr  string            `json:"remote_addr"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body,omitempty"`
	Response    ResponseLog       `json:"response"`
	Duration    time.Duration     `json:"duration"`
	UserAgent   string            `json:"user_agent"`
	ContentType string            `json:"content_type"`
	Size        int64             `json:"size"`
}

type ResponseLog struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Size       int64             `json:"size"`
}

type LoggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int64
	headers    map[string]string
}

func (lrw *LoggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *LoggingResponseWriter) Write(b []byte) (int, error) {
	if lrw.statusCode == 0 {
		lrw.statusCode = 200
	}
	size, err := lrw.ResponseWriter.Write(b)
	lrw.size += int64(size)
	return size, err
}

func (lrw *LoggingResponseWriter) Header() http.Header {
	return lrw.ResponseWriter.Header()
}

func (lrw *LoggingResponseWriter) captureHeaders() {
	lrw.headers = make(map[string]string)
	for k, v := range lrw.ResponseWriter.Header() {
		lrw.headers[k] = strings.Join(v, ", ")
	}
}

type TGateServer struct {
	logger      *zap.Logger
	sugarLogger *zap.SugaredLogger
	proxy       *httputil.ReverseProxy
	targetURL   *url.URL
	requestLog  []RequestLog
	logMutex    sync.RWMutex
}

func NewTGateServer(logger *zap.Logger, targetPort int) *TGateServer {
	targetURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("localhost:%d", targetPort),
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Customize the director to preserve original headers
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", req.Host)
	}

	return &TGateServer{
		logger:      logger,
		sugarLogger: logger.Sugar(),
		proxy:       proxy,
		targetURL:   targetURL,
		requestLog:  make([]RequestLog, 0),
	}
}

func (s *TGateServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Create logging response writer
	lrw := &LoggingResponseWriter{
		ResponseWriter: w,
		statusCode:     0,
		size:           0,
		headers:        make(map[string]string),
	}

	// Read request body for logging (if not too large)
	var bodyBytes []byte
	var bodyString string
	if r.Body != nil && r.ContentLength < 10*1024*1024 { // Limit to 10MB
		bodyBytes, _ = io.ReadAll(r.Body)
		bodyString = string(bodyBytes)
		r.Body = io.NopCloser(strings.NewReader(bodyString))
	}

	// Capture request headers
	reqHeaders := make(map[string]string)
	for k, v := range r.Header {
		reqHeaders[k] = strings.Join(v, ", ")
	}

	// Log incoming request
	s.sugarLogger.Infow("Incoming request",
		"method", r.Method,
		"url", r.URL.String(),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.UserAgent(),
		"content_length", r.ContentLength,
	)

	// Print request details to console
	s.printRequestDetails(r, reqHeaders, bodyString)

	// Serve the request
	s.proxy.ServeHTTP(lrw, r)

	// Capture response headers after serving
	lrw.captureHeaders()

	duration := time.Since(start)

	// Create request log entry
	logEntry := RequestLog{
		Timestamp:   start,
		Method:      r.Method,
		URL:         r.URL.String(),
		RemoteAddr:  r.RemoteAddr,
		Headers:     reqHeaders,
		Body:        bodyString,
		UserAgent:   r.UserAgent(),
		ContentType: r.Header.Get("Content-Type"),
		Size:        r.ContentLength,
		Response: ResponseLog{
			StatusCode: lrw.statusCode,
			Headers:    lrw.headers,
			Size:       lrw.size,
		},
		Duration: duration,
	}

	// Store log entry
	s.logMutex.Lock()
	s.requestLog = append(s.requestLog, logEntry)
	// Keep only last 1000 requests
	if len(s.requestLog) > 1000 {
		s.requestLog = s.requestLog[1:]
	}
	s.logMutex.Unlock()

	// Log response
	s.sugarLogger.Infow("Response sent",
		"status_code", lrw.statusCode,
		"response_size", lrw.size,
		"duration", duration,
	)

	// Print response summary
	s.printResponseSummary(lrw.statusCode, lrw.size, duration)
}

func (s *TGateServer) printRequestDetails(r *http.Request, headers map[string]string, body string) {
	fmt.Printf("\n╭─ %s %s\n", r.Method, r.URL.String())
	fmt.Printf("├─ From: %s\n", r.RemoteAddr)
	fmt.Printf("├─ Time: %s\n", time.Now().Format("15:04:05"))

	if len(headers) > 0 {
		fmt.Printf("├─ Headers:\n")

		// Sort headers for consistent display
		var sortedHeaders []string
		for k := range headers {
			sortedHeaders = append(sortedHeaders, k)
		}
		sort.Strings(sortedHeaders)

		for i, k := range sortedHeaders {
			prefix := "│  "
			if i == len(sortedHeaders)-1 && body == "" {
				prefix = "│  "
			}
			fmt.Printf("%s%s: %s\n", prefix, k, headers[k])
		}
	}

	if body != "" && len(body) < 1000 { // Only show small bodies
		fmt.Printf("├─ Body:\n")
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			prefix := "│  "
			if i == len(lines)-1 {
				prefix = "│  "
			}
			fmt.Printf("%s%s\n", prefix, line)
		}
	} else if body != "" {
		fmt.Printf("├─ Body: [%d bytes - too large to display]\n", len(body))
	}

	fmt.Printf("╰─ Proxying to %s\n", s.targetURL.String())
}

func (s *TGateServer) printResponseSummary(statusCode int, size int64, duration time.Duration) {
	statusIcon := "✓"
	if statusCode >= 400 {
		statusIcon = "✗"
	}

	fmt.Printf("   %s %d • %s • %d bytes\n",
		statusIcon,
		statusCode,
		duration.Round(time.Millisecond),
		size)
}

func (s *TGateServer) GetRequestLogs() []RequestLog {
	s.logMutex.RLock()
	defer s.logMutex.RUnlock()

	// Return a copy
	logs := make([]RequestLog, len(s.requestLog))
	copy(logs, s.requestLog)
	return logs
}

func setupLogger(verbose bool, jsonFormat bool, logFile string) (*zap.Logger, error) {
	var config zap.Config

	if jsonFormat {
		config = zap.NewProductionConfig()
	} else {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	if verbose {
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	} else {
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	if logFile != "" {
		config.OutputPaths = []string{logFile, "stdout"}
	}

	return config.Build()
}

func enableHTTPSFeature(ctx context.Context, lc *local.Client, sugar *zap.SugaredLogger) error {
	// Check if HTTPS is already enabled
	status, err := lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	if status.Self.HasCap(tailcfg.CapabilityHTTPS) {
		sugar.Infof("✅ HTTPS capability already enabled")
		return nil
	}

	sugar.Infof("🔍 HTTPS capability not enabled, need to enable it...")
	sugar.Infof("💡 This will enable HTTPS certificate provisioning for your tailnet")
	sugar.Infof("💡 Go to https://login.tailscale.com/admin/dns and enable 'HTTPS Certificates'")
	sugar.Infof("💡 Or wait while we try to enable it automatically...")
	
	// Try to enable HTTPS capability
	// Note: This might require admin permissions or interactive approval
	// The exact API for this isn't publicly documented, so we'll provide guidance
	
	return fmt.Errorf("HTTPS capability needs to be enabled in your Tailscale admin console")
}

func checkTailscaleCertificates(ctx context.Context, lc *local.Client, dnsName string, sugar *zap.SugaredLogger) error {
	// Check if HTTPS certificates are available
	status, err := lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	// Check if the node has HTTPS capability
	if !status.Self.HasCap(tailcfg.CapabilityHTTPS) {
		sugar.Warnf("❌ Node does not have HTTPS capability enabled")
		sugar.Infof("💡 To enable HTTPS certificates:")
		sugar.Infof("   1. Go to https://login.tailscale.com/admin/dns")
		sugar.Infof("   2. Enable 'HTTPS Certificates' for your tailnet")
		sugar.Infof("   3. Wait a few minutes for certificate provisioning")
		return fmt.Errorf("HTTPS certificates not enabled for this tailnet")
	}

	sugar.Infof("✅ HTTPS capability is enabled for this tailnet")

	// Check certificate status - this is a bit tricky as the API doesn't directly expose cert status
	// We can try to check if certificates exist by looking at the certificate domains
	if len(status.CertDomains) == 0 {
		sugar.Warnf("⚠️  No certificate domains found")
		sugar.Infof("💡 Certificate provisioning may still be in progress")
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
		sugar.Warnf("⚠️  Certificate not found for domain %s", dnsName)
		sugar.Infof("💡 Available certificate domains: %v", status.CertDomains)
		sugar.Infof("💡 Certificate provisioning may still be in progress")
		return fmt.Errorf("certificate not available for domain %s", dnsName)
	}

	sugar.Infof("✅ Certificate appears to be available for %s", dnsName)
	return nil
}

func findAvailablePort(startPort int) (int, error) {
	for port := startPort; port < startPort+100; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found starting from %d", startPort)
}

func setupTailscaleServe(ctx context.Context, lc *local.Client, proxyPort int, mountPath string, enableFunnel bool, useHTTPS bool, servePort int, sugar *zap.SugaredLogger) error {
	// Get current serve config
	sc, err := lc.GetServeConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get serve config: %w", err)
	}
	if sc == nil {
		sc = new(ipn.ServeConfig)
	}

	// Get local client status for DNS name
	st, err := lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")

	// Set up HTTP handler for the proxy target (pointing to our local logging proxy)
	h := &ipn.HTTPHandler{
		Proxy: fmt.Sprintf("http://localhost:%d", proxyPort),
	}

	// Clean mount path
	if mountPath == "" {
		mountPath = "/"
	}
	if !strings.HasPrefix(mountPath, "/") {
		mountPath = "/" + mountPath
	}

	// Determine serve port and TLS usage
	var srvPort uint16
	var useTLS bool
	
	if servePort != 0 {
		srvPort = uint16(servePort)
		useTLS = useHTTPS || servePort == 443
	} else {
		if useHTTPS {
			srvPort = 443
			useTLS = true
		} else {
			srvPort = 80
			useTLS = false
		}
	}

	sugar.Infof("Setting up Tailscale serve on port %d (TLS: %t)", srvPort, useTLS)

	// Check if port is already in use
	if sc.IsTCPForwardingOnPort(srvPort) {
		return fmt.Errorf("port %d is already serving TCP", srvPort)
	}

	// Set web handler
	sc.SetWebHandler(h, dnsName, srvPort, mountPath, useTLS)

	// If using HTTPS/TLS, we need to also set up the TCP handler for TLS termination
	if useTLS {
		sugar.Infof("🔍 Setting up HTTPS TCP handler for TLS termination...")
		if sc.TCP == nil {
			sc.TCP = make(map[uint16]*ipn.TCPPortHandler)
		}
		sc.TCP[srvPort] = &ipn.TCPPortHandler{
			HTTPS: true,
		}
		
		if err := enableHTTPSFeature(ctx, lc, sugar); err != nil {
			sugar.Warnf("⚠️  HTTPS feature check failed: %v", err)
			sugar.Infof("💡 HTTPS may not work properly without certificates")
			sugar.Infof("💡 Consider using HTTP mode instead: remove --use-https flag")
		}
	}

	// Enable funnel if requested (only works with HTTPS/443)
	if enableFunnel {
		if !useTLS || srvPort != 443 {
			sugar.Warnf("Funnel requires HTTPS on port 443, but serving on port %d with TLS=%t", srvPort, useTLS)
			sugar.Infof("Consider using --use-https or --serve-port=443")
			return fmt.Errorf("funnel requires HTTPS on port 443")
		}

		// Enable HTTPS feature first if needed
		if err := enableHTTPSFeature(ctx, lc, sugar); err != nil {
			sugar.Errorf("❌ Failed to enable HTTPS feature: %v", err)
			sugar.Infof("💡 Please enable HTTPS certificates in your Tailscale admin console:")
			sugar.Infof("   1. Go to https://login.tailscale.com/admin/dns")
			sugar.Infof("   2. Enable 'HTTPS Certificates'")
			sugar.Infof("   3. Wait a few minutes for provisioning")
			sugar.Infof("   4. Try again")
			return fmt.Errorf("HTTPS certificates not enabled: %w", err)
		}

		// Check certificate status before enabling funnel
		sugar.Infof("🔍 Checking HTTPS certificate status for funnel...")
		if err := checkTailscaleCertificates(ctx, lc, dnsName, sugar); err != nil {
			sugar.Errorf("❌ Certificate check failed: %v", err)
			sugar.Infof("💡 You can:")
			sugar.Infof("   • Wait a few minutes if certificates are still provisioning")
			sugar.Infof("   • Try running without --funnel first to test local access")
			sugar.Infof("   • Check https://login.tailscale.com/admin/dns for certificate settings")
			return fmt.Errorf("cannot enable funnel: %w", err)
		}

		sc.SetFunnel(dnsName, srvPort, true)
		sugar.Infof("🌍 Funnel enabled - service will be available on the internet")
	}

	// Apply the serve config
	err = lc.SetServeConfig(ctx, sc)
	if err != nil {
		return fmt.Errorf("failed to set serve config: %w", err)
	}

	// Display URL information with certificate status
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	
	portPart := ""
	if (scheme == "http" && srvPort != 80) || (scheme == "https" && srvPort != 443) {
		portPart = fmt.Sprintf(":%d", srvPort)
	}

	url := fmt.Sprintf("%s://%s%s%s", scheme, dnsName, portPart, mountPath)
	
	if enableFunnel {
		sugar.Infof("🌍 Available on the internet: %s", url)
		sugar.Infof("💡 If you get TLS errors, certificates may still be provisioning")
		sugar.Infof("💡 Try again in 2-3 minutes if the connection fails")
	} else {
		sugar.Infof("🔒 Available within your tailnet: %s", url)
		if useTLS {
			sugar.Infof("💡 If you get TLS errors, try HTTP first or wait for certificate provisioning")
		}
	}

	return nil
}

func cleanupTailscaleServe(ctx context.Context, lc *local.Client, port int, mountPath string, useHTTPS bool, servePort int, sugar *zap.SugaredLogger) error {
	sc, err := lc.GetServeConfig(ctx)
	if err != nil || sc == nil {
		return nil // Nothing to clean up
	}

	st, err := lc.Status(ctx)
	if err != nil {
		return err
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")

	if mountPath == "" {
		mountPath = "/"
	}
	if !strings.HasPrefix(mountPath, "/") {
		mountPath = "/" + mountPath
	}

	// Determine the port we used for serving
	var srvPort uint16
	if servePort != 0 {
		srvPort = uint16(servePort)
	} else {
		if useHTTPS {
			srvPort = 443
		} else {
			srvPort = 80
		}
	}

	sc.RemoveWebHandler(dnsName, srvPort, []string{mountPath}, true)

	err = lc.SetServeConfig(ctx, sc)
	if err != nil {
		sugar.Warnf("Failed to cleanup serve config: %v", err)
	} else {
		sugar.Infof("Cleaned up Tailscale serve configuration")
	}

	return nil
}

func main() {
	kong.Parse(&CLI)

	// Setup logger
	logger, err := setupLogger(CLI.Verbose, CLI.JSON, CLI.LogFile)
	if err != nil {
		fmt.Printf("Failed to setup logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	sugar := logger.Sugar()

	// If funnel is enabled, automatically enable HTTPS since funnel requires it
	if CLI.Funnel {
		CLI.UseHTTPS = true
		sugar.Infof("🌍 Funnel enabled - automatically enabling HTTPS")
	}

	sugar.Infof("Starting tgate server...")
	sugar.Infof("Local target: localhost:%d", CLI.Port)
	sugar.Infof("Funnel enabled: %t", CLI.Funnel)
	sugar.Infof("HTTPS enabled: %t", CLI.UseHTTPS)

	// Test local connection
	testConn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", CLI.Port), 5*time.Second)
	if err != nil {
		sugar.Fatalf("Cannot connect to local server at localhost:%d - %v", CLI.Port, err)
	}
	testConn.Close()
	sugar.Infof("✓ Local server is reachable")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Determine which mode to use
	useLocalTailscale := false
	var localClient *local.Client

	if !CLI.ForceTsnet && CLI.AuthKey == "" {
		// Try to use local Tailscale
		localClient = &local.Client{}
		_, err := localClient.Status(ctx)
		if err == nil {
			useLocalTailscale = true
			sugar.Infof("✓ Using local Tailscale daemon")
		} else {
			sugar.Infof("Local Tailscale not available: %v", err)
			sugar.Infof("Falling back to tsnet mode")
		}
	}

	if CLI.AuthKey != "" {
		sugar.Infof("Auth key provided - using tsnet mode")
	}

	if CLI.ForceTsnet {
		sugar.Infof("Forced tsnet mode")
	}

	var cleanup func() error

	if useLocalTailscale {
		// Create our logging proxy server
		tgateServer := NewTGateServer(logger, CLI.Port)
		
		// Find an available port for our local logging proxy
		proxyPort, err := findAvailablePort(CLI.Port + 1000)
		if err != nil {
			sugar.Fatalf("Failed to find available port for logging proxy: %v", err)
		}
		
		sugar.Infof("Starting local logging proxy on port %d", proxyPort)
		
		// Start our logging proxy server
		proxyServer := &http.Server{
			Addr:    fmt.Sprintf("localhost:%d", proxyPort),
			Handler: tgateServer,
		}
		
		go func() {
			if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				sugar.Errorf("Logging proxy server error: %v", err)
			}
		}()
		
		// Give the proxy server a moment to start
		time.Sleep(100 * time.Millisecond)
		
		// Use local Tailscale serve (pointing to our logging proxy)
		sugar.Infof("Setting up Tailscale serve...")
		
		err = setupTailscaleServe(ctx, localClient, proxyPort, CLI.SetPath, CLI.Funnel, CLI.UseHTTPS, CLI.ServePort, sugar)
		if err != nil {
			sugar.Fatalf("Failed to setup Tailscale serve: %v", err)
		}

		cleanup = func() error {
			// Cleanup Tailscale serve config
			cleanupTailscaleServe(context.Background(), localClient, CLI.Port, CLI.SetPath, CLI.UseHTTPS, CLI.ServePort, sugar)
			// Shutdown proxy server
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return proxyServer.Shutdown(shutdownCtx)
		}

		sugar.Infof("🚀 tgate server configured with Tailscale serve + logging proxy")
		sugar.Infof("🔍 All requests will be logged and forwarded to localhost:%d", CLI.Port)
	} else {
		// Use tsnet mode
		tgateServer := NewTGateServer(logger, CLI.Port)
		
		httpServer := &http.Server{
			Handler: tgateServer,
		}

		var tsnetServer *tsnet.Server
		if CLI.AuthKey != "" {
			tsnetServer = &tsnet.Server{
				Hostname: CLI.TailscaleName,
				AuthKey:  CLI.AuthKey,
			}
		} else {
			tsnetServer = &tsnet.Server{
				Hostname: CLI.TailscaleName,
			}
		}

		sugar.Infof("Tailscale node name: %s", CLI.TailscaleName)

		ln, err := tsnetServer.Listen("tcp", ":80")
		if err != nil {
			sugar.Fatalf("Failed to listen on Tailscale device: %v", err)
		}

		// Get the device's Tailscale URL
		status, err := tsnetServer.Up(ctx)
		if err != nil {
			sugar.Warnf("Could not get device status: %v", err)
		} else {
			tailscaleURL := fmt.Sprintf("https://%s", status.Self.DNSName)
			sugar.Infof("📡 Tailscale URL: %s", tailscaleURL)
		}

		cleanup = func() error {
			httpServer.Shutdown(context.Background())
			ln.Close()
			tsnetServer.Close()
			return nil
		}

		go func() {
			sugar.Infof("🚀 tgate server started with tsnet")
			if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
				sugar.Errorf("HTTP server error: %v", err)
			}
		}()
	}

	// Display running information
	fmt.Printf("\n" + strings.Repeat("─", 60) + "\n")
	if useLocalTailscale {
		fmt.Printf("  tgate is running with Tailscale serve!\n")
		fmt.Printf("  Mode: Local Tailscale daemon\n")
	} else {
		fmt.Printf("  tgate is running with tsnet!\n")
		fmt.Printf("  Mode: tsnet device (%s)\n", CLI.TailscaleName)
	}
	fmt.Printf("  Target: localhost:%d\n", CLI.Port)
	if CLI.WebUI {
		fmt.Printf("  Web UI: Planned feature\n")
	}
	fmt.Printf(strings.Repeat("─", 60) + "\n\n")

	// Wait for shutdown signal
	<-ctx.Done()
	sugar.Infof("Shutting down tgate server...")

	if cleanup != nil {
		if err := cleanup(); err != nil {
			sugar.Errorf("Error during cleanup: %v", err)
		}
	}

	sugar.Infof("tgate server stopped")
}