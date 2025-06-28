package middleware

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jaxxstorm/tgate/internal/model"
)

// LoggingResponseWriter wraps http.ResponseWriter to capture response information
type LoggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int64
	headers    map[string]string
}

// WriteHeader captures the status code
func (lrw *LoggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

// Write captures the response size
func (lrw *LoggingResponseWriter) Write(b []byte) (int, error) {
	if lrw.statusCode == 0 {
		lrw.statusCode = 200
	}
	size, err := lrw.ResponseWriter.Write(b)
	lrw.size += int64(size)
	return size, err
}

// Header returns the response headers
func (lrw *LoggingResponseWriter) Header() http.Header {
	return lrw.ResponseWriter.Header()
}

// captureHeaders captures response headers for logging
func (lrw *LoggingResponseWriter) captureHeaders() {
	lrw.headers = make(map[string]string)
	for k, v := range lrw.ResponseWriter.Header() {
		lrw.headers[k] = strings.Join(v, ", ")
	}
}

// AccessLog returns middleware that logs HTTP requests and responses
func AccessLog(callback func(model.RequestLog)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			
			// Serve the request
			next.ServeHTTP(lrw, r)
			
			// Capture response headers after serving
			lrw.captureHeaders()
			
			duration := time.Since(start)
			
			// Create request log entry
			logEntry := model.RequestLog{
				ID:          generateRequestID(),
				Timestamp:   start,
				Method:      r.Method,
				URL:         r.URL.String(),
				RemoteAddr:  r.RemoteAddr,
				Headers:     reqHeaders,
				Body:        bodyString,
				UserAgent:   r.UserAgent(),
				ContentType: r.Header.Get("Content-Type"),
				Size:        r.ContentLength,
				StatusCode:  lrw.statusCode,
				Response: model.ResponseLog{
					StatusCode: lrw.statusCode,
					Headers:    lrw.headers,
					Size:       lrw.size,
				},
				Duration: duration,
			}
			
			// Call the callback with the log entry
			if callback != nil {
				callback(logEntry)
			}
		})
	}
}

// generateRequestID generates a unique request ID
func generateRequestID() string {
	return "req_" + time.Now().Format("20060102150405") + "_" + randomString(6)
}

// randomString generates a random string of the specified length
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}

// CORS returns middleware that adds CORS headers
func CORS(origins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			
			// Check if origin is allowed
			allowed := false
			for _, allowedOrigin := range origins {
				if allowedOrigin == "*" || allowedOrigin == origin {
					allowed = true
					break
				}
			}
			
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID returns middleware that adds a unique request ID to the request context
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := generateRequestID()
			w.Header().Set("X-Request-ID", requestID)
			
			// Add request ID to request headers for downstream services
			r.Header.Set("X-Request-ID", requestID)
			
			next.ServeHTTP(w, r)
		})
	}
}

// Recovery returns middleware that recovers from panics and logs them
func Recovery() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("Internal Server Error"))
				}
			}()
			
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout returns middleware that enforces a timeout on requests
func Timeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, timeout, "Request Timeout")
	}
}