package httputil

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// NewForwardingProxy creates a new reverse proxy that forwards requests to the target URL
func NewForwardingProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	
	// Customize the director to preserve original headers
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Set forwarded headers for better traceability
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-For", req.RemoteAddr)
	}
	
	return proxy
}

// ProxyConfig holds configuration for creating proxies
type ProxyConfig struct {
	Target          *url.URL
	PreserveHost    bool
	CustomHeaders   map[string]string
	RemoveHeaders   []string
	ModifyResponse  func(*http.Response) error
}

// NewCustomProxy creates a reverse proxy with custom configuration
func NewCustomProxy(config ProxyConfig) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(config.Target)
	
	// Customize the director
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		
		// Preserve original host if requested
		if config.PreserveHost {
			req.Host = config.Target.Host
		}
		
		// Add custom headers
		for key, value := range config.CustomHeaders {
			req.Header.Set(key, value)
		}
		
		// Remove specified headers
		for _, header := range config.RemoveHeaders {
			req.Header.Del(header)
		}
		
		// Set standard forwarded headers
		req.Header.Set("X-Forwarded-Proto", getScheme(req))
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-For", req.RemoteAddr)
	}
	
	// Set custom response modifier if provided
	if config.ModifyResponse != nil {
		proxy.ModifyResponse = config.ModifyResponse
	}
	
	return proxy
}

// getScheme determines the scheme from the request
func getScheme(req *http.Request) string {
	if req.TLS != nil {
		return "https"
	}
	if scheme := req.Header.Get("X-Forwarded-Proto"); scheme != "" {
		return scheme
	}
	return "http"
}