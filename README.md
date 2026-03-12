# HTTP Shadow Proxy

A high-performance HTTP proxy written in Go that mirrors a percentage of incoming traffic to a shadow backend for testing and comparison purposes, while serving requests via a primary backend.

## Features

- **Traffic Shadowing**: Mirrored requests to a shadow backend without affecting the primary response.
- **Protocol Modes**: Supports HTTP shadow proxying and a TCP Milter proxy for mail filter testing.
- **Sampling & Rate Limiting**: Configure the percentage of traffic to shadow and apply rate limits (RPS/Burst) to protect the shadow environment.
- **Response Comparison**: Compares headers and optional payloads between primary and shadow responses and logs differences.
- **Structured Logging**: Uses `slog` for detailed, machine-readable logs including performance metrics and header diffs.
- **TLS Support**: Supports both incoming TLS and secure communication with upstream backends (with custom CA support).
- **Concurrency Control**: Managed worker pools and queues for both primary and shadow backends.

## Configuration

The proxy uses a YAML configuration file loaded via `viper`. By default, it looks for `config.yaml` in the current working directory or `/etc/doppelgaenger`. You can override the location with the `CONFIG_FILE` environment variable.

### Proxy Configuration (`config.yaml`)

The configuration is categorized into global, protocol-specific, and feature-specific settings.

#### Global & Common Settings
These settings apply to both HTTP and Milter protocols unless otherwise specified.

- `protocol`: Selects the proxy protocol (`http` or `milter`).
- `tls_cert_file` / `tls_key_file`: Paths to the TLS certificate and key for the proxy server.
- `root_ca`: Path to a common CA certificate for all upstream backends.
- `primary_root_ca` / `shadow_root_ca`: CA certificates for specific backends.
- `insecure_upstream`: Allows insecure TLS connections (no verification) to backends.
- `shadow_sample_percent`: Percentage of traffic to mirror (0-100).
- `shadow_rps`: Max requests per second for the shadow backend (0 disables).
- `shadow_burst`: Token bucket burst capacity for shadow rate limiting.
- `compare_headers`: List of headers compared between primary and shadow responses.
- `log_json`: Enables structured JSON logging.
- `log_session_only_on_diff`: Logs session-specific headers only when a difference is detected.
- `run_as_user`: Optional target user (name or numeric UID) after startup.
- `run_as_group`: Optional primary group (name or numeric GID) after startup.
- `chroot`: Optional root directory for process isolation (must contain needed runtime files).

#### HTTP-specific Settings
- `listen_addr`: The address the HTTP proxy listens on (e.g., `:8443`).
- `primary_base_url`: Legacy single primary backend URL.
- `primary_base_urls`: List of primary backend URLs.
- `primary_selection_mode`: Primary selection strategy (`round_robin` or `source_ip_hash`).
- `shadow_base_url`: Target URL for the shadow backend.
- `shadow_timeout`: Time limit for requests to the shadow backend.
- `shadow_force_header`: Header that forces shadowing for the current request.
- `primary_request_headers`: Optional static request headers added only to primary backend requests.
- `shadow_request_headers`: Optional static request headers added only to shadow backend requests.
- `primary_workers` / `shadow_workers`: Number of parallel workers for each pool.
- `primary_queue` / `shadow_queue`: Maximum queue size for backend requests.
- `max_backend_body_bytes`: Maximum request body size forwarded to backends.
- `forward_response_headers`: Headers passed from the primary backend to the client.
- `compare_mode`: Selection of the comparison engine (`nginx`, `header`, `json`, `html`).
- `compare_json_strict`: Enables strict mode for JSON comparison.
- `compare_html_threshold`: Similarity threshold for HTML comparison.
- `path_mapping`: Advanced path rewriting rules (see [Path Mapping](#path-mapping-modes)).

#### Milter-specific Settings
- `milter_listen_addr`: TCP address for the Milter proxy listener.
- `primary_milter_addr` / `shadow_milter_addr`: TCP addresses of the Milter backends.
- `milter_timeout`: Timeout for Milter upstream operations.

### Configuration Example

```yaml
# Runtime hardening (optional)
# run_as_user: "nobody"
# run_as_group: "nogroup"
# chroot: "/var/empty/doppelgaenger"

protocol: http # http or milter
listen_addr: ":8443"
primary_base_url: "https://127.0.0.1:9001"
primary_base_urls:
  - "https://127.0.0.1:9001"
  - "https://127.0.0.1:9003"
primary_selection_mode: "round_robin" # round_robin or source_ip_hash
shadow_base_url: "https://127.0.0.1:9002"

# Shadow settings
shadow_sample_percent: 5
shadow_timeout: "150ms"
shadow_rps: 200.0
shadow_burst: 400
shadow_force_header: "X-Shadow"
primary_request_headers:
  X-Backend-Target: primary
shadow_request_headers:
  X-Backend-Target: shadow

# Backend settings
primary_workers: 32
shadow_workers: 16
primary_queue: 4096
shadow_queue: 4096
max_backend_body_bytes: 32768

# Comparison settings
compare_mode: nginx # nginx, header, json, html
compare_headers:
  - "Auth-Status"
  - "Auth-Server"
compare_json_strict: false
compare_html_threshold: 0.99

# Logging
log_json: true
log_session_only_on_diff: true

# TLS (optional)
# tls_cert_file: "/path/to/cert.pem"
# tls_key_file: "/path/to/key.pem"
# root_ca: "/path/to/ca.pem"
# insecure_upstream: false

# Path Mapping
path_mapping:
  mode: rewrite # direct or rewrite
  rules:
    - match: "^/api/v1/(.*)$"
      primary: "/v1/$1"
      shadow: "/legacy/$1"
```

#### Path Mapping Modes

- **direct**: Forwards the incoming request path unchanged to both primary and shadow backends.
- **rewrite**: Applies a list of regex-based rules. The first matching rule is applied.
    - `match`: A regular expression to match the incoming path.
    - `primary`: Replacement string for the primary backend path (supports regex groups like `$1`).
    - `shadow`: Replacement string for the shadow backend path (supports regex groups like `$1`).

If no rule matches in `rewrite` mode, or if `primary`/`shadow` fields are empty, the original path is used.

#### Runtime Privilege/Chroot Notes

- `chroot` and user/group switching require elevated privileges (typically root).
- The process applies security settings in this order: `chroot` → `setgroups` → `setgid` → `setuid`.
- Supplementary groups are derived automatically from `run_as_user` (all groups of that user).
- If required runtime files are missing inside the jail (`/etc/hosts`, `/etc/resolv.conf`, `/etc/nsswitch.conf`), startup fails with a descriptive error.

## Fake Server

The fake server is a standalone program intended to act as primary and shadow backends for testing. It mirrors incoming request headers into response headers and can optionally randomize selected headers.

### Fake Server Configuration (`fakehttpserver.yaml`)
```yaml
listen_addr: ":9001"
mode: "echo"
random_chance: 10
log_json: true
```

## How It Works

1. **Request Arrival**: The proxy receives an HTTP(S) request.
2. **Primary Request**: The request is forwarded to one backend from `primary_base_urls` according to `primary_selection_mode`. The response from that backend is returned to the client.
3. **Shadow Decision**: Based on `shadow_sample_percent` or the presence of `shadow_force_header`, the proxy decides whether to shadow the request.
4. **Shadow Request**: If selected, the request is sent to the `shadow_base_url` backend asynchronously.
5. **Comparison**: The proxy compares headers and (optionally) payloads between primary and shadow responses based on `compare_mode`.
6. **Logging**: A single structured log line is generated containing details about both requests, including durations and any header differences found.

### Milter Mode
When `protocol: milter`, the proxy listens on `milter_listen_addr` and forwards incoming Milter frames to the primary backend, mirrors them to the shadow backend, compares decisions/raw frames, and logs any differences.

## Building and Running

### Prerequisites
- Go 1.25 or later

### Build
```bash
go build -o doppelgaenger main.go
go build -o fakehttpserver cmd/fakehttpserver/main.go
```

### SBOM
```bash
make sbom
```
Generates `sbom.cdx.json` in the project directory. During the Docker image build, the SBOM is copied into the image as `/app/sbom.cdx.json`.

### Run
```bash
# edit config.yaml (or set CONFIG_FILE to another YAML file)
./doppelgaenger
```

For Docker usage, `docker-compose.yml` mounts `config.docker.yaml` and the fake server configs (`fakehttpserver-primary.yaml`, `fakehttpserver-shadow.yaml`).

### Run Fake Server
```bash
# edit fakehttpserver.yaml (or set CONFIG_FILE to another YAML file)
./fakehttpserver
```

## License

[Add License Information Here, e.g., MIT]
