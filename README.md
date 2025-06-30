# 🌐 TGate

**A beautiful HTTP proxy and testing tool that exposes local services through Tailscale with comprehensive request logging and monitoring.**

TGate combines the power of Tailscale's secure networking with an elegant terminal user interface for real-time request monitoring. Perfect for development, debugging, webhook testing, and sharing local services securely.

## ✨ Features

- 🔒 **Share Local Services Securely** - Expose your development servers to teammates through private Tailscale network
- 🌍 **Make Local Apps Public** - Share your local applications with anyone on the internet using Tailscale Funnel
- 🎭 **Test Webhooks Instantly** - Create public mock endpoints for webhook testing without running any backend
- 🕵️ **Monitor HTTP Traffic** - See all requests and responses in real-time with a beautiful terminal interface
- � **Zero Configuration Setup** - Auto-configures HTTPS, certificates, and ports - just run one command
- 📊 **Debug API Issues** - Inspect complete request/response details including headers, timing, and payload
- 🎯 **Cross-Platform Testing** - Test your local APIs from mobile devices, external services, or different networks
- 🔧 **Development Workflow** - Perfect for frontend/backend integration, mobile app testing, and API development
- �️ **Production-Ready** - Reliable networking with automatic cleanup and error recovery

[![asciicast](https://asciinema.org/a/kbVHNsESs757fnOtyB7mFcefQ.svg)](https://asciinema.org/a/kbVHNsESs757fnOtyB7mFcefQ)

## 🚀 Installation

### Homebrew (macOS/Linux)

```bash
# Add the tap
brew tap jaxxstorm/tap

# Install tgate
brew install tgate
```

### Pre-built Binaries

Download the latest release for your platform from the [GitHub Releases](https://github.com/jaxxstorm/tgate/releases) page.

#### Linux
```bash
# Download and extract (replace with latest version and your platform)
curl -L https://github.com/jaxxstorm/tgate/releases/download/v0.1.0/tgate-v0.1.0-linux-amd64.tar.gz | tar xz

# Make executable and move to PATH
chmod +x tgate
sudo mv tgate /usr/local/bin/
```

#### MacOS

```bash
brew install jaxxstorm/tap/tgate
```

#### Windows
Download the `.zip` file from releases and extract `tgate.exe` to a directory in your PATH.

### From Source

```bash
# Clone the repository
git clone https://github.com/jaxxstorm/tgate.git
cd tgate

# Install dependencies
go mod tidy

# Build
go build -o tgate main.go

# Install to PATH (optional)
sudo mv tgate /usr/local/bin/
```

## 🏃 Quick Start

### Verify Installation

```bash
# Check version
tgate --version

# View help
tgate --help
```

### Basic Usage

```bash
# Expose a local service running on port 8080
tgate 8080

# Enable Tailscale Funnel (public internet access)
tgate 8080 --funnel

# Mock/testing mode for webhook testing (auto-enables funnel)
tgate --mock

# Use legacy console output instead of TUI
tgate 8080 --no-tui

# Verbose logging
tgate 8080 --funnel --verbose

# Clean up Tailscale serve configuration
tgate --cleanup-serve
```

## 📖 Usage Examples

### Expose a Local Web Server

```bash
# Start your local development server
python -m http.server 8080

# In another terminal, expose it through Tailscale
tgate 8080
```

### Share a Local API Publicly

```bash
# Expose your local API to the internet
tgate 3000 --funnel
```

### Test Webhooks (Mock Mode)

```bash
# Create a public mock endpoint for webhook testing
# This automatically enables funnel for external access
tgate --mock

# With verbose logging to see all request details
tgate --mock --verbose

# With custom settings
tgate --mock --no-tui --set-path /webhooks
```

### Debug Webhook Endpoints with Real Server

```bash
# Expose your local webhook server to the internet
tgate 4000 --funnel --verbose
```

### Custom Configuration

```bash
# Use HTTPS on auto-allocated port with a specific path
tgate 8080 --use-https --set-path /api

# Force tsnet mode with custom node name
tgate 8080 --force-tsnet --tailscale-name my-proxy-node

# Clean up any existing Tailscale serve configuration
tgate --cleanup-serve
```

## 🎛 Command Line Options

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `PORT` | - | Local port to expose (required for proxy mode) | - |
| `--mock` | `-m` | Enable mock/testing mode (no backing server, auto-enables funnel) | `false` |
| `--version` | - | Show version information | - |
| `--tailscale-name` | `-n` | Tailscale node name (tsnet mode only) | `tgate` |
| `--funnel` | `-f` | Enable Tailscale funnel (public internet) | `false` |
| `--verbose` | `-v` | Enable verbose logging | `false` |
| `--json` | `-j` | Output logs in JSON format | `false` |
| `--no-tui` | - | Disable TUI, use console output | `false` |
| `--log-file` | - | Write logs to file | - |
| `--auth-key` | - | Tailscale auth key for tsnet mode | - |
| `--force-tsnet` | - | Force tsnet mode | `false` |
| `--set-path` | - | Custom serve path | `/` |
| `--serve-port` | - | Tailscale serve port (auto-configured) | `443` (funnel), `80` (tailnet) |
| `--use-https` | - | Use HTTPS (auto-enabled with --funnel) | `false` |
| `--cleanup-serve` | - | Clean up Tailscale serve config and exit | `false` |

## 🎭 Mock Mode

Mock mode is perfect for testing webhooks and APIs without needing a real backend server:

### Features
- **No backing server required** - TGate responds to all requests with a 200 OK
- **Auto-enables funnel** - Automatically makes the endpoint publicly accessible
- **Request logging** - All requests are logged with full details
- **JSON responses** - Returns structured JSON with request metadata

### Example Response
```json
{
  "status": "received",
  "timestamp": "2024-01-15T10:30:45Z",
  "method": "POST",
  "path": "/webhook",
  "headers": 12,
  "body_size": 156,
  "query": "token=abc123",
  "content_type": "application/json"
}
```

### Usage Scenarios
- **Webhook development** - Test webhook delivery from external services
- **API prototyping** - Mock API endpoints during development
- **Integration testing** - Verify HTTP client behavior
- **Debugging** - Inspect request format and headers

## 🎨 Terminal User Interface

TGate features a beautiful split-pane TUI built with [Charm](https://charm.sh) libraries:

### Left Pane: Application Logs
- **Unified logging format** - All logs (application, proxy, Tailscale) use consistent timestamp and level formatting
- **Structured logging** - Color-coded log levels (INFO, WARN, ERROR) with component identification
- **Real-time updates** - Live application status, configuration, and request/response logs
- **Scrollable history** - Navigate through log history with automatic retention management

### Right Pane: Request Monitor
- Latest HTTP request details with full context
- Request method, URL, path, and status code
- Response timing and size information
- Complete request and response headers
- Request body content (for POST/PUT requests)
- Color-coded status indicators and error highlighting

### Enhanced Features
- **Smart port allocation** - Automatic selection of available ports for reliability
- **Service URL display** - Shows all access URLs (internet, tailnet, local, UI)
- **Readiness checks** - Waits for services to be fully ready before displaying URLs
- **Clean shutdown** - Automatic cleanup of Tailscale serve configuration on exit

### Controls
- `q` or `Ctrl+C` to quit (with automatic cleanup)
- Automatic scrolling and responsive window resizing
- Up/Down arrows to scroll through content

## 🔧 Configuration

### Prerequisites

1. **Install Tailscale** on your machine:
   ```bash
   # macOS
   brew install tailscale
   
   # Linux (Ubuntu/Debian)
   curl -fsSL https://tailscale.com/install.sh | sh
   
   # Windows
   # Download from https://tailscale.com/download
   ```

2. **Authenticate with Tailscale**:
   ```bash
   sudo tailscale up
   ```

3. **Enable HTTPS certificates** (for --funnel or --use-https):
   - Go to https://login.tailscale.com/admin/dns
   - Enable "HTTPS Certificates"
   - Wait 2-3 minutes for certificate provisioning

### Operating Modes

#### Proxy Mode (Default)
Forwards requests to a local server:
```bash
tgate 8080 --funnel
```

#### Mock Mode
Creates a mock server for testing:
```bash
# Mock mode (auto-enables funnel)
tgate --mock
```

#### Local Tailscale Mode
Uses your existing Tailscale installation:
```bash
tgate 8080 --funnel
```

#### TSNet Mode
Creates a separate Tailscale device:
```bash
# Interactive authentication
tgate 8080 --force-tsnet

# With auth key
tgate 8080 --auth-key tskey-auth-xxxxx
```

## 🌍 Tailscale Funnel

Tailscale Funnel allows you to expose services to the public internet with automatic configuration:

```bash
# Enable funnel (automatically configures HTTPS on port 443)
tgate 8080 --funnel

# Mock mode automatically enables funnel with optimal settings
tgate --mock
```

**Requirements:**
- HTTPS certificates enabled in Tailscale admin console
- Valid Tailscale subscription with Funnel access
- **Automatic configuration**: TGate automatically:
  - Enables HTTPS when funnel is requested
  - Configures port 443 for funnel traffic
  - Sets up proper certificate handling
  - Manages Tailscale serve configuration

**Auto-Configuration Benefits:**
- No manual port configuration needed
- Automatic HTTPS certificate setup
- Proper funnel/serve configuration management
- Clean shutdown with configuration cleanup

## 📝 Logging

### Unified Logging Architecture
TGate uses structured logging with consistent formatting across all components:
- **Proxy server logs** - HTTP request/response details with method, path, and remote address
- **Application logs** - Startup, configuration, and status information
- **Tailscale logs** - Connection status and certificate management
- **All logs** use the same timestamp format and log level indicators

### TUI Mode (Default)
- **Split-pane interface** with real-time visual updates
- **Unified log formatting** - All components log with consistent timestamp/level format
- **Color-coded information** with component identification
- **Automatic retention** - Maintains history of recent requests and logs
- **Request/response tracking** - Full HTTP transaction logging in the application logs pane

### Console Mode
```bash
# Use traditional console output with structured logging
tgate 8080 --no-tui
```

### File Logging
```bash
# Write all logs to file with structured format
tgate 8080 --log-file /tmp/tgate.log
```

### JSON Format
```bash
# Structured JSON output for log analysis tools
tgate 8080 --json --log-file /tmp/tgate.json
```

### Enhanced Logging Features
- **Structured fields** - Consistent log field names across components
- **Component identification** - Clear source identification for each log entry
- **Request correlation** - HTTP requests appear in main application logs
- **Error context** - Enhanced error messages with actionable information

## 🛠 Development

### Prerequisites
- Go 1.21+
- Tailscale installed and authenticated

### Architecture Overview
TGate is built with a modular architecture for maintainability:

```
internal/
├── config/          # CLI parsing and validation
├── server/          # Server setup and readiness checks
├── proxy/           # HTTP proxy with structured logging
├── tailscale/       # Tailscale client and configuration
├── tui/             # Terminal UI and unified logging
├── logging/         # Structured logging utilities
├── httputil/        # HTTP utilities and readiness checks
├── middleware/      # HTTP middleware
├── model/           # Data types and structures
├── stats/           # Request statistics tracking
└── ui/              # Web UI server
```

### Building from Source
```bash
# Clone the repository
git clone https://github.com/jaxxstorm/tgate.git
cd tgate

# Install dependencies
go mod tidy

# Build
go build -o tgate main.go

# Run with development settings
./tgate 8080 --verbose
```

### Key Design Principles
- **Modular architecture** - Clear separation of concerns between components
- **Structured logging** - Consistent logging format across all components
- **Robust networking** - Automatic port allocation and readiness checks
- **Clean shutdown** - Proper resource cleanup and Tailscale serve management
- **Error handling** - Comprehensive validation with actionable error messages

### Dependencies
- [Kong](https://github.com/alecthomas/kong) - CLI parsing and validation
- [Zap](https://go.uber.org/zap) - Structured logging framework
- [Tailscale](https://tailscale.com) - Secure networking and automatic certificates
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - Terminal UI framework
- [Lipgloss](https://github.com/charmbracelet/lipgloss) - Terminal UI styling and components

### Release Process

This project uses [GoReleaser](https://goreleaser.com/) for automated releases:

```bash
# Tag a new version
git tag v0.1.0
git push origin v0.1.0

# GoReleaser will automatically:
# - Build binaries for all platforms
# - Create GitHub release
# - Update Homebrew tap
```

## 🎯 Use Cases

### Development & Testing
- **Local development** - Expose development servers with automatic port management
- **Webhook testing** - Use mock mode for instant, publicly accessible endpoints
- **API prototyping** - Mock endpoints with structured logging and request inspection
- **Integration testing** - Test client implementations with comprehensive request tracking
- **Cross-team collaboration** - Share local services securely within your Tailscale network

### Production & Deployment
- **Staging environments** - Temporary public access for testing and demo purposes
- **Load testing** - Monitor request patterns and performance with detailed metrics
- **Debug production issues** - Proxy production traffic for detailed analysis
- **Service migration** - Gradual traffic shifting with request monitoring

### Debugging & Monitoring
- **HTTP traffic analysis** - Real-time monitoring with unified logging format
- **Request inspection** - Complete headers, body, and timing information
- **Performance debugging** - Response time and request size tracking
- **Client behavior testing** - Verify HTTP client implementations and configurations
- **Webhook payload verification** - Inspect webhook formats and delivery patterns

### Security & Compliance
- **Secure external access** - Leverage Tailscale's authentication and encryption
- **Audit logging** - Comprehensive request logs with structured format
- **Access control** - Use Tailscale ACLs for fine-grained permissions
- **Certificate management** - Automatic HTTPS with proper certificate handling

## 🔒 Security

TGate leverages Tailscale's security model with enhanced configuration management:

- **Private by default** - Only accessible within your Tailscale network
- **End-to-end encryption** - All traffic is encrypted via Tailscale
- **Auto-certificate management** - Automatic TLS certificate provisioning and renewal
- **Access controls** - Leverage Tailscale's ACL system for fine-grained permissions
- **Audit logs** - Comprehensive request logging with structured format
- **Clean configuration** - Automatic cleanup prevents configuration drift
- **Secure defaults** - HTTPS automatically enabled for funnel mode

## ⚡ Reliability & Performance

TGate includes several robustness improvements for production use:

### Smart Port Management
- **Random port allocation** - Automatic selection of available ephemeral ports
- **Port conflict resolution** - Intelligent handling of port allocation failures
- **Service readiness checks** - Waits for services to be fully ready before announcing URLs

### Enhanced Error Handling
- **Validation at startup** - Comprehensive CLI argument and configuration validation
- **Graceful degradation** - Fallback options when preferred configurations aren't available
- **Clear error messages** - Actionable error information with troubleshooting guidance

### Configuration Management
- **Automatic cleanup** - Tailscale serve configuration cleaned up on shutdown
- **Configuration validation** - Prevents invalid or conflicting settings
- **State management** - Proper handling of Tailscale serve/funnel state transitions

### Monitoring & Observability
- **Unified logging** - All components use consistent structured logging format
- **Request correlation** - HTTP transactions tracked through the entire stack
- **Performance metrics** - Request timing and response size tracking
- **Health indicators** - Service status and readiness information

## 📜 License

MIT License - see LICENSE file for details.

## 🤝 Contributing

Contributions welcome! Please feel free to submit a Pull Request.

## 🐛 Troubleshooting

### Installation Issues
```bash
# Verify installation
tgate --version

# Check if Tailscale is installed and running
tailscale status

# Test with mock mode first (simplest setup)
tgate --mock
```

### TUI Issues
```bash
# Use console mode if TUI has display problems
tgate 8080 --no-tui --funnel

# Enable verbose logging for debugging
tgate 8080 --no-tui --verbose
```

### HTTPS Certificate Issues
```bash
# Check if HTTPS is enabled in Tailscale admin console
# Wait 2-3 minutes after enabling certificates in admin console

# Test HTTP first to verify connectivity
tgate 8080  # Without --funnel or --use-https

# For funnel, certificates are configured automatically
tgate 8080 --funnel --verbose

# Mock mode handles all certificate setup automatically
tgate --mock --verbose
```

### Port and Configuration Issues
```bash
# Clean up any existing Tailscale serve configuration
tgate --cleanup-serve

# Use mock mode which handles all configuration automatically
tgate --mock

# For proxy mode, verify local service is running first
curl localhost:8080
tgate 8080 --verbose
```

### Connection and Network Issues
```bash
# Check Tailscale connection status
tailscale status

# Verify local service is accessible
curl localhost:8080  # Replace with your port

# Test with minimal configuration first
tgate 8080 --verbose

# Use mock mode to isolate networking issues
tgate --mock --verbose

# Check logs for detailed error information
tgate 8080 --no-tui --verbose --log-file /tmp/tgate.log
```

### Cleanup and Reset
```bash
# Clean up Tailscale serve configuration
tgate --cleanup-serve

# Reset Tailscale serve (alternative method)
tailscale serve reset

# Restart with fresh configuration
tgate --mock --verbose
```

---

**Made with ❤️ for the Tailscale community**