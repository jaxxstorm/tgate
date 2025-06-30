package httputil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// WaitForServerReady waits for a server to be ready by checking if it can accept connections
func WaitForServerReady(ctx context.Context, address string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for server at %s to be ready", address)
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

// WaitForHTTPServerReady waits for an HTTP server to be ready by making a health check request
func WaitForHTTPServerReady(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout: 100 * time.Millisecond,
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for HTTP server at %s to be ready", url)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				continue
			}

			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				return nil
			}
		}
	}
}
