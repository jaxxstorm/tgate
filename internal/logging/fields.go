// internal/logging/fields.go
package logging

import (
	"time"
	
	"go.uber.org/zap"
)

// Common field constructors for consistent logging
func ServerMode(mode string) zap.Field {
	return zap.String("server_mode", mode)
}

func Port(port int) zap.Field {
	return zap.Int("port", port)
}

func TargetPort(port int) zap.Field {
	return zap.Int("target_port", port)
}

func ProxyPort(port int) zap.Field {
	return zap.Int("proxy_port", port)
}

func UIPort(port int) zap.Field {
	return zap.Int("ui_port", port)
}

func TailscalePort(port int) zap.Field {
	return zap.Int("tailscale_port", port)
}

func FunnelEnabled(enabled bool) zap.Field {
	return zap.Bool("funnel_enabled", enabled)
}

func HTTPSEnabled(enabled bool) zap.Field {
	return zap.Bool("https_enabled", enabled)
}

func UIEnabled(enabled bool) zap.Field {
	return zap.Bool("ui_enabled", enabled)
}

func TUIEnabled(enabled bool) zap.Field {
	return zap.Bool("tui_enabled", enabled)
}

func MockMode(enabled bool) zap.Field {
	return zap.Bool("mock_mode", enabled)
}

func TailscaleMode(mode string) zap.Field {
	return zap.String("tailscale_mode", mode)
}

func NodeName(name string) zap.Field {
	return zap.String("node_name", name)
}

func MountPath(path string) zap.Field {
	return zap.String("mount_path", path)
}

func BindAddress(addr string) zap.Field {
	return zap.String("bind_address", addr)
}

func URL(url string) zap.Field {
	return zap.String("url", url)
}

func Duration(d time.Duration) zap.Field {
	return zap.Duration("duration", d)
}

func Error(err error) zap.Field {
	return zap.Error(err)
}

func Status(status string) zap.Field {
	return zap.String("status", status)
}

func Component(component string) zap.Field {
	return zap.String("component", component)
}

func Operation(op string) zap.Field {
	return zap.String("operation", op)
}

func StartPort(port int) zap.Field {
	return zap.Int("start_port", port)
}

func ServePort(port int) zap.Field {
	return zap.Int("serve_port", port)
}

func Version(version string) zap.Field {
	return zap.String("version", version)
}

func LocalPort(port int) zap.Field {
	return zap.Int("local_port", port)
}