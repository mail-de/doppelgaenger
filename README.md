# HTTP Shadow Proxy

A high-performance HTTP proxy written in Go that mirrors a percentage of incoming traffic to a shadow backend for testing and comparison purposes, while serving requests via a primary backend.

## Features

- **Traffic Shadowing**: Mirrored requests to a shadow backend without affecting the primary response.
- **Protocol Modes**: Supports HTTP shadow proxying and a TCP Milter proxy for mail filter testing.
- **Sampling & Rate Limiting**: Configure the percentage of traffic to shadow and apply rate limits (RPS/Burst) to protect the shadow environment.
- **Response Comparison**: Compares headers and optional payloads between primary and shadow responses and logs differences.
- **Structured Logging**: Uses `slog` for detailed, machine-readable logs including performance metrics and header diffs.
- **Observability**: Optional OpenMetrics/Prometheus endpoint plus OpenTelemetry OTLP traces and metrics for ingress, primary, shadow, and comparison paths.
- **TLS Support**: Supports both incoming TLS and secure communication with upstream backends (with custom CA support).

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
- `listen_addr`: The address the HTTP proxy listens on (e.g., `:8080`).
- `primary_base_urls`: List of primary backend URLs.
- `primary_selection_mode`: Primary selection strategy (`round_robin` or `source_ip_hash`).
- `shadow_base_urls`: List of shadow backend URLs.
- `shadow_selection_mode`: Shadow selection strategy (`round_robin` or `source_ip_hash`).
- `shadow_timeout`: Time limit for requests to the shadow backend.
- `shadow_force_header`: Header that forces shadowing for the current request.
- `primary_request_headers`: Optional static request headers added only to primary backend requests.
- `shadow_request_headers`: Optional static request headers added only to shadow backend requests.
- `max_backend_body_bytes`: Maximum request body size forwarded to backends.
- `upstream_http_dial_timeout`: Timeout for establishing upstream TCP connections.
- `upstream_http_tls_handshake_timeout`: Timeout for upstream TLS handshakes.
- `upstream_http_response_header_timeout`: Timeout for receiving upstream response headers.
- `upstream_http_max_idle_conns`: Max idle keep-alive connections across all upstream hosts.
- `upstream_http_max_idle_conns_per_host`: Max idle keep-alive connections per upstream host.
- `upstream_http_max_conns_per_host`: Max total upstream connections per host (`0` means unlimited).
- `upstream_http_protocol`: Upstream protocol mode (`auto`, `http1`, or `http2`). `auto` negotiates HTTP/2 when the upstream offers it; `http1` disables HTTP/2; `http2` rejects an HTTP/1.1 fallback.
- `forward_response_headers`: Headers passed from the primary backend to the client.
- `compare_mode`: Selection of the comparison engine (`nginx`, `header`, `json`, `html`).
- `compare_json_strict`: Enables strict mode for JSON comparison.
- `compare_html_threshold`: Similarity threshold for HTML comparison.
- `path_rules`: Ordered HTTP path policy for per-route shadowing and comparison.
- `path_mapping`: Advanced path rewriting rules (see [Path Mapping](#path-mapping-modes)).

#### Milter-specific Settings
- `milter_listen_addr`: TCP address for the Milter proxy listener.
- `primary_milter_addr` / `shadow_milter_addr`: TCP addresses of the Milter backends.
- `milter_timeout`: Timeout for Milter upstream operations.

#### Observability Settings
- `observability.prometheus_enabled`: Starts the dedicated OpenMetrics/Prometheus scrape endpoint.
- `observability.prometheus_address` / `observability.prometheus_port` / `observability.prometheus_path`: Bind settings for the scrape endpoint.
- `observability.prometheus_runtime_metrics`: Adds Go runtime and process collectors.
- `observability.prometheus_http_auth_basic`: Optional `user:password` Basic Auth for the scrape endpoint.
- `observability.prometheus_tls`: Optional server-side TLS for the scrape endpoint (`enabled`, `cert`, `key`, `min_tls_version`).
- `observability.otel_enabled`: Master switch for OpenTelemetry export.
- `observability.otel_traces_enabled` / `observability.otel_metrics_enabled`: Separate OTLP trace and metric export switches.
- `observability.otel_service_name` / `observability.otel_service_version`: Resource identity for exported telemetry.
- `observability.otel_exporter_otlp_endpoint`: OTLP HTTP collector endpoint, required when OpenTelemetry is enabled.
- `observability.otel_exporter_otlp_headers`: Optional headers for the OTLP exporter.
- `observability.otel_exporter_otlp_insecure`: Uses insecure OTLP HTTP transport.
- `observability.otel_sample_ratio`: Parent-based trace sampling ratio from `0.0` to `1.0`.
- `observability.trace_id_header`: Bare trace ID header added to responses and outgoing HTTP primary/shadow requests; `traceparent` is always the standard W3C propagation carrier.

### Configuration Example

```yaml
# Runtime hardening (optional)
# run_as_user: "nobody"
# run_as_group: "nogroup"
# chroot: "/var/empty/doppelgaenger"

protocol: http # http or milter
listen_addr: ":8080"
primary_base_urls:
  - "https://127.0.0.1:9001"
  - "https://127.0.0.1:9003"
primary_selection_mode: "round_robin" # round_robin or source_ip_hash
shadow_base_urls:
  - "https://127.0.0.1:9002"
  - "https://127.0.0.1:9004"
shadow_selection_mode: "round_robin" # round_robin or source_ip_hash

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

max_backend_body_bytes: 32768
upstream_http_dial_timeout: "2s"
upstream_http_tls_handshake_timeout: "5s"
upstream_http_response_header_timeout: "5s"
upstream_http_max_idle_conns: 1024
upstream_http_max_idle_conns_per_host: 256
upstream_http_max_conns_per_host: 0
upstream_http_protocol: auto

# Comparison settings
compare_mode: nginx # nginx, header, json, html
compare_headers:
  - "Auth-Status"
  - "Auth-Server"
compare_json_strict: false
compare_html_threshold: 0.99

# HTTP path rules are optional. An absent or empty list preserves the global
# shadow and comparison behavior above.
path_rules: []

# Example: shadow one JSON API route, keep metrics and health primary-only,
# and make all other routes primary-only through an explicit catch-all.
# path_rules:
#   - name: json-api
#     methods: ["POST"]
#     match: "^/api/v1/json$"
#     shadow: auto
#     compare: on
#     compare_mode: json
#     compare_headers:
#       - "Content-Type"
#       - "X-Api-Status"
#
#   - name: metrics-health
#     match: "^/(metrics|healthz)$"
#     shadow: never
#     compare: off
#
#   - name: default-primary-only
#     match: "^/.*$"
#     shadow: never
#     compare: off

# Logging
log_json: true
log_session_only_on_diff: true

# Observability
observability:
  prometheus_enabled: false
  prometheus_address: "127.0.0.1"
  prometheus_port: 9464
  prometheus_path: "/metrics"
  prometheus_runtime_metrics: false
  # prometheus_http_auth_basic: "metrics:change-me"
  prometheus_tls:
    enabled: false
    # cert: "/etc/doppelgaenger/metrics.crt"
    # key: "/etc/doppelgaenger/metrics.key"
    min_tls_version: "1.2"
  otel_enabled: false
  otel_traces_enabled: false
  otel_metrics_enabled: false
  otel_service_name: "doppelgaenger"
  # otel_service_version defaults to the binary version.
  # otel_exporter_otlp_endpoint: "http://127.0.0.1:4318"
  # otel_exporter_otlp_headers:
  #   authorization: "Bearer token"
  otel_exporter_otlp_insecure: false
  otel_sample_ratio: 1.0
  trace_id_header: "X-Trace-ID"

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

#### HTTP Path Rules

`path_rules` is an optional top-level HTTP-only list. When it is absent or
empty, Doppelgaenger keeps the existing global behavior from
`shadow_sample_percent`, `shadow_force_header`, `shadow_rps`,
`shadow_burst`, `compare_mode`, and `compare_headers`.

When rules are configured, they are evaluated in order and the first rule that
matches both `methods` and `match` wins. If no rule matches, the request is
primary-only: no shadow request is started and comparison is skipped. Add an
explicit catch-all rule such as `match: "^/.*$"` when the remaining path space
should keep a deliberate fallback policy.

Rule fields:

- `name`: Optional stable name used in logs. If omitted, a deterministic name
  such as `rule[0]` is used.
- `methods`: Optional HTTP method list. Values are normalized to uppercase.
  Empty or omitted means all methods.
- `match`: Required regular expression matched against the inbound request path
  only. Query strings are not part of matching.
- `shadow`: Optional shadow policy. Defaults to `inherit`.
- `compare`: Optional comparison policy. Defaults to `inherit`.
- `compare_mode`: Optional per-rule comparison engine (`nginx`, `header`,
  `json`, or `html`). If omitted, the global `compare_mode` is used.
- `compare_headers`: Optional per-rule response header list. If omitted, the
  global `compare_headers` list is used. If explicitly set to `[]`, no headers
  are compared for that rule.

Supported `shadow` values:

- `inherit`: Use the current global shadow decision.
- `auto`: Allow normal sampling and `shadow_force_header`.
- `never`: Do not shadow, even when the force header is present.
- `always`: Start shadowing without sampling when the rule matches. The
  `shadow_rps` and `shadow_burst` rate limiter still protects the shadow
  backend.

Supported `compare` values:

- `inherit`: Compare when shadowing runs, using global comparison behavior.
- `on`: Compare when shadowing runs, using the per-rule `compare_mode` when set
  or the global mode otherwise.
- `off`: Skip comparison even if the shadow request ran.

Path-rule matching uses the inbound request path before `path_mapping` rewrites
the path for primary and shadow backends. The force header can still bypass the
shadow rate limiter on allowed paths, preserving the global override behavior;
`shadow: never` is authoritative and wins over the force header.

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
3. **Shadow Decision**: Based on the first matching `path_rules` entry, or on global `shadow_sample_percent` and `shadow_force_header` when no rules are configured, the proxy decides whether to shadow the request.
4. **Shadow Request**: If selected, the request is mirrored to one backend from `shadow_base_urls` according to `shadow_selection_mode` asynchronously and outside the client response path.
5. **Comparison**: The proxy compares headers and (optionally) payloads between primary and shadow responses based on the resolved path rule or the global comparison settings.
6. **Observability & Logging**: A single structured log line is generated containing details about both requests, including durations and any header differences found. When tracing is active, outgoing HTTP primary/shadow requests receive W3C `traceparent` plus the configured bare trace-ID header.

### Milter Mode
When `protocol: milter`, the proxy listens on `milter_listen_addr` and forwards incoming Milter frames to the primary backend, mirrors them to the shadow backend, compares decisions/raw frames, and logs any differences.

## Observability

Observability is disabled by default. When enabled, the proxy records:

- Incoming HTTP requests and Milter frames with outcome, duration, and shadow-start labels.
- Outgoing Primary and Shadow backend exchanges with protocol, target, method/command, status/decision, result, and duration.
- Primary/Shadow comparison outcomes (`same`, `diff`, `error`, `skipped`).

The Prometheus endpoint uses `promhttp` with OpenMetrics negotiation enabled. Example:

```yaml
observability:
  prometheus_enabled: true
  prometheus_address: "127.0.0.1"
  prometheus_port: 9464
  prometheus_path: "/metrics"
  prometheus_http_auth_basic: "metrics:secret"
```

OpenTelemetry uses OTLP over HTTP:

```yaml
observability:
  otel_enabled: true
  otel_traces_enabled: true
  otel_metrics_enabled: true
  otel_exporter_otlp_endpoint: "http://127.0.0.1:4318"
  otel_exporter_otlp_insecure: true
  otel_sample_ratio: 1.0
```

Incoming HTTP `traceparent` is extracted and continued. Outgoing HTTP requests to Primary and Shadow receive the active W3C trace context and the configured `trace_id_header` value. In Milter mode there is no standard header carrier, so the proxy emits spans/metrics for the TCP frame and backend exchanges but does not invent protocol payload fields.

## Building and Running

### Prerequisites
- Go 1.26.3 or later

### Command-line Options
```bash
./doppelgaenger --config /path/to/config.yaml
./doppelgaenger --version
./doppelgaenger --help
```

- `--config`, `-c`: Uses the given YAML config file and takes precedence over `CONFIG_FILE`.
- `--version`: Prints the build version injected by the Makefile or Docker build and exits.
- `--help`, `-h`: Prints usage information.

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

### E2E Checks
```bash
make e2e-http
make e2e-milter
make e2e-docker
make e2e
```

The E2E checks build local test binaries and run real doppelgaenger processes
against fake Primary/Shadow backends. The HTTP check verifies W3C trace context
and `X-Trace-ID` propagation, Primary/Shadow routing, path rewriting, request
IDs, per-target headers, body limits, OpenMetrics counters, and OTLP
trace/metric export. The Milter check verifies Primary/Shadow frame forwarding,
no-shadow sampling, Milter comparison metrics, and OTLP spans/metrics without
mutating Milter payloads. The Docker check verifies that the image, compose
mapping, and mounted runtime config agree on the exposed listener.

### Run
```bash
# edit config.yaml (or set CONFIG_FILE to another YAML file)
./doppelgaenger
```

For Docker usage, `docker-compose.yml` mounts `config.docker.yaml` and the fake server configs (`fakehttpserver-primary.yaml`, `fakehttpserver-shadow.yaml`).

### systemd Socket Activation (Zero-Downtime Friendly)

The project includes:
- `doppelgaenger.socket`: owns the public listen socket (`8080`).
- `doppelgaenger.service`: runs the proxy and consumes activated sockets via `LISTEN_FDS`.

Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now doppelgaenger.socket doppelgaenger.service
```

Reload/rollout recommendation:
```bash
sudo systemctl restart doppelgaenger.service
```
With socket activation enabled, the listen socket stays available while the service is replaced. The old process drains in-flight HTTP requests via graceful shutdown (`http.Server.Shutdown`).

`systemctl reload doppelgaenger.service` is configured to trigger an asynchronous service restart (`try-restart`) so admins can keep using `reload` operationally while still getting the robust restart path.

### Run Fake Server
```bash
# edit fakehttpserver.yaml (or set CONFIG_FILE to another YAML file)
./fakehttpserver
```

## License

[Add License Information Here, e.g., MIT]
