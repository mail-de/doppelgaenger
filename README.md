# Doppelgaenger Shadow Proxy

A high-performance proxy written in Go that mirrors a percentage of incoming traffic to a shadow backend for testing and comparison purposes, while serving responses from a primary backend.

## Features

- **Traffic Shadowing**: Mirrored requests to a shadow backend without affecting the primary response.
- **Protocol Modes**: Supports HTTP shadow proxying, generic gRPC shadow proxying, and a TCP Milter proxy for mail filter testing.
- **Sampling & Rate Limiting**: Configure the percentage of traffic to shadow and apply rate limits (RPS/Burst) to protect the shadow environment.
- **Response Comparison**: Compares HTTP headers/payloads, gRPC status/metadata/message counts/message hashes, or Milter decisions and logs differences.
- **Structured Logging**: Uses `slog` for detailed, machine-readable logs including performance metrics and header diffs.
- **Observability**: Optional OpenMetrics/Prometheus endpoint plus OpenTelemetry OTLP traces and metrics for ingress, primary, shadow, and comparison paths.
- **TLS Support**: Supports both incoming TLS and secure communication with upstream backends (with custom CA support).

## Configuration

The proxy uses a YAML configuration file loaded via `viper`. By default, it looks for `config.yaml` in the current working directory or `/etc/doppelgaenger`. You can override the location with the `CONFIG_FILE` environment variable.

### Proxy Configuration (`config.yaml`)

The configuration is categorized into global, protocol-specific, and feature-specific settings.

#### Global & Common Settings
These settings apply across protocols unless a protocol-specific section says otherwise.

- `protocol`: Selects the proxy protocol (`http`, `grpc`, or `milter`).
- `tls_cert_file` / `tls_key_file`: Paths to the TLS certificate and key for the HTTP listener.
- `root_ca`: Path to a common CA certificate for HTTP upstream backends.
- `primary_root_ca` / `shadow_root_ca`: CA certificates for specific HTTP backends.
- `insecure_upstream`: Allows insecure TLS connections (no verification) to HTTP backends.
- `shadow_sample_percent`: Percentage of traffic to mirror (0-100).
- `shadow_rps`: Max requests per second for the shadow backend (0 disables).
- `shadow_burst`: Token bucket burst capacity for shadow rate limiting.
- `compare_headers`: List of headers compared between primary and shadow responses.
- `log_json`: Enables structured JSON logging.
- `log_only_on_diff`: Emits proxy result logs only for differences, shadow errors, or comparison errors.
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
- `primary_request_headers`: Optional static request headers added only to primary backend requests; `path_rules[].primary_request_headers` can override them for matched primary calls.
- `shadow_request_headers`: Optional static request headers added only to shadow backend requests; `path_rules[].shadow_request_headers` can override them for matched shadow calls.
- `max_backend_body_bytes`: Maximum request body size forwarded to backends.
- `upstream_http_dial_timeout`: Timeout for establishing upstream TCP connections.
- `upstream_http_tls_handshake_timeout`: Timeout for upstream TLS handshakes.
- `upstream_http_response_header_timeout`: Timeout for receiving upstream response headers.
- `upstream_http_max_idle_conns`: Max idle keep-alive connections across all upstream hosts.
- `upstream_http_max_idle_conns_per_host`: Max idle keep-alive connections per upstream host.
- `upstream_http_max_conns_per_host`: Max total upstream connections per host (`0` means unlimited).
- `upstream_http_protocol`: Upstream protocol mode (`auto`, `http1`, or `http2`). `auto` negotiates HTTP/2 when the upstream offers it; `http1` disables HTTP/2; `http2` rejects an HTTP/1.1 fallback.
- `forward_response_headers`: Headers passed from the primary backend to the client. Keep protocol-visible headers such as `Content-Type`, `Content-Encoding`, `Vary`, cache controls, `WWW-Authenticate`, `Location`, and `Set-Cookie` for browser and OIDC clients. Redirect `Location` and `Set-Cookie` are preserved for 3xx responses so browser-facing OIDC flows are not consumed by the proxy.
- `compare_mode`: Selection of the comparison engine (`header`, `json`, `html`; legacy `nginx` is accepted as an alias for `header`).
- `compare_json_strict`: Enables strict mode for JSON comparison.
- `compare_html_threshold`: Similarity threshold for HTML comparison.
- `path_rules`: Ordered HTTP path policy for per-route shadowing and comparison.
- `path_mapping`: Advanced path rewriting rules (see [Path Mapping](#path-mapping-modes)).

#### Milter-specific Settings
- `milter_listen_addr`: TCP address for the Milter proxy listener.
- `primary_milter_addr` / `shadow_milter_addr`: TCP addresses of the Milter backends.
- `milter_timeout`: Timeout for Milter upstream operations. Shadow Milter work
  is asynchronous and serialized per client connection; it never delays the
  Primary response. If the bounded shadow queue fills or the shadow session
  fails, shadowing stops for that connection while Primary remains fail-closed.

#### gRPC-specific Settings
- `grpc_listen_addr`: TCP address for the gRPC proxy listener. Plaintext gRPC uses HTTP/2 prior knowledge when `grpc_tls.enabled` is false.
- `grpc_tls`: Inbound listener TLS/mTLS (`enabled`, `cert`, `key`, `client_ca`, `require_client_cert`, `min_tls_version`). `client_ca` plus `require_client_cert` enables client certificate verification.
- `primary_grpc_targets` / `shadow_grpc_targets`: Upstream gRPC target pools. Each target has `name`, `address`, optional `authority`, and `tls` settings (`enabled`, `root_ca`, `server_name`, `client_cert`, `client_key`, `insecure_skip_verify`).
- `grpc_backend_oidc_auth`: Optional client-credentials Bearer token injection for primary gRPC upstream calls. Configure either `configuration_uri` or `token_endpoint`, `client_id`, and exactly one of `client_secret` or `client_secret_env`.
- `primary_grpc_selection_mode` / `shadow_grpc_selection_mode`: Target selection strategy (`round_robin` or `source_ip_hash`).
- `grpc_shadow_timeout`: Best-effort lifetime for shadow gRPC streams only. The primary stream is not constrained by this timeout.
- `grpc_shadow_force_metadata`: Incoming metadata key that forces shadowing when present and non-empty, unless the matched rule says `shadow: never`.
- `grpc_shadow_queue_size`: Bounded request-message queue for shadow forwarding. When full, the primary stream continues and the shadow path is marked `queue_full`.
- `grpc_max_receive_message_bytes` / `grpc_max_send_message_bytes`: Message size limits applied to inbound and outbound gRPC calls.
- `grpc_compare_mode`: Default comparison mode (`status`, `status_metadata`, `message_count`, or `message_hash`).
- `grpc_compare_metadata`: Metadata/trailer keys compared by `status_metadata`. Use explicit allowlists; arbitrary request metadata is not logged.
- `grpc_rules`: Ordered service/method policy for gRPC shadowing, comparison, and primary/shadow metadata overlays.

#### Observability Settings
- `observability.prometheus_enabled`: Starts the dedicated OpenMetrics/Prometheus scrape endpoint.
- `observability.prometheus_address` / `observability.prometheus_port` / `observability.prometheus_path`: Bind settings for the scrape endpoint.
- `/healthz`: When the Prometheus server is enabled and its metrics path is not `/healthz`, this readiness endpoint is served on the same address without Prometheus Basic Auth.
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
- `observability.trace_id_header`: Bare trace ID header/metadata added to outgoing HTTP and gRPC primary/shadow requests; `traceparent` is always the standard W3C propagation carrier.

### Configuration Example

```yaml
# Runtime hardening (optional)
# run_as_user: "nobody"
# run_as_group: "nogroup"
# chroot: "/var/empty/doppelgaenger"

protocol: http # http, grpc, or milter
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

forward_response_headers:
  - "Auth-Status"
  - "Auth-Server"
  - "Auth-Port"
  - "Auth-User"
  - "Auth-Pass"
  - "Auth-Error"
  - "Auth-Wait"
  - "Auth-Protocol"
  - "X-Nauthilus-Session"
  - "Location"
  - "Set-Cookie"
  - "Content-Type"
  - "Cache-Control"
  - "Pragma"
  - "Expires"
  - "WWW-Authenticate"
  - "Content-Encoding"
  - "Vary"

# Comparison settings
compare_mode: header # header, json, html; nginx is accepted as a legacy alias
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
#     primary_request_headers:
#       X-Route-Backend: json-primary
#     shadow_request_headers:
#       X-Route-Backend: json-shadow
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
log_only_on_diff: false
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

### gRPC Configuration Example

```yaml
protocol: grpc
grpc_listen_addr: ":9444"
grpc_tls:
  enabled: true
  cert: "/etc/doppelgaenger/grpc.crt"
  key: "/etc/doppelgaenger/grpc.key"
  client_ca: "/etc/doppelgaenger/clients-ca.pem"
  require_client_cert: true
  min_tls_version: "1.2"

primary_grpc_targets:
  - name: primary
    address: "primary.example.net:9443"
    authority: "primary.example.net"
    tls:
      enabled: true
      root_ca: "/etc/doppelgaenger/primary-ca.pem"
      server_name: "primary.example.net"
      # client_cert: "/etc/doppelgaenger/primary-client.crt"
      # client_key: "/etc/doppelgaenger/primary-client.key"

shadow_grpc_targets:
  - name: shadow
    address: "shadow.example.net:9443"
    authority: "shadow.example.net"
    tls:
      enabled: true
      root_ca: "/etc/doppelgaenger/shadow-ca.pem"
      server_name: "shadow.example.net"
      # client_cert: "/etc/doppelgaenger/shadow-client.crt"
      # client_key: "/etc/doppelgaenger/shadow-client.key"

shadow_sample_percent: 0
shadow_rps: 200
shadow_burst: 400
grpc_shadow_force_metadata: "x-shadow"
grpc_shadow_timeout: "500ms"
grpc_shadow_queue_size: 128
grpc_compare_mode: status
grpc_compare_metadata:
  - "grpc-status"
  - "grpc-message"

grpc_backend_oidc_auth:
  enabled: true
  configuration_uri: "https://login.example.net/.well-known/openid-configuration"
  # token_endpoint: "https://login.example.net/oauth2/token"
  client_id: "doppelgaenger-primary"
  client_secret_env: "DOPPELGAENGER_GRPC_CLIENT_SECRET"
  auth_method: auto
  scopes:
    - "nauthilus:authenticate"
  timeout: 5s
  refresh_skew: 30s

grpc_rules:
  - name: auth-shadow
    service: "nauthilus.auth.v1.AuthService"
    methods: ["Authenticate", "LookupIdentity", "ListAccounts"]
    shadow: auto
    compare: on
    compare_mode: status_metadata
    compare_metadata:
      - "grpc-status"
      - "grpc-message"
      - "x-nauthilus-session"
    primary_metadata:
      authorization: "Basic primary-token"
    shadow_metadata:
      authorization: "Basic shadow-token"

  - name: default-primary-only
    service: "*"
    shadow: never
    compare: off
```

gRPC targets are not configured with HTTP URLs. Use `address` for the gRPC authority endpoint and `tls` for transport security. Metadata overlays are gRPC metadata, not HTTP headers; binary metadata (`*-bin`) and transport-controlled keys such as `grpc-*`, `content-type`, `te`, and pseudo-headers are rejected.

When `grpc_backend_oidc_auth.enabled` is true, Doppelgaenger obtains a client-credentials access token and adds `authorization: Bearer ...` to primary gRPC upstream calls. Do not also configure `primary_metadata.authorization` in `grpc_rules`; static primary authorization metadata conflicts with the dynamic token source. `auth_method: auto` currently resolves to `client_secret_basic` when a client secret source is configured, or set `client_secret_post` explicitly when the token endpoint requires form credentials.

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
- `compare_mode`: Optional per-rule comparison engine (`header`, `json`, or
  `html`; legacy `nginx` is accepted as an alias for `header`). If omitted, the
  global `compare_mode` is used.
- `compare_headers`: Optional per-rule response header list. If omitted, the
  global `compare_headers` list is used. If explicitly set to `[]`, no headers
  are compared for that rule.
- `primary_request_headers`: Optional per-rule request header overlay for
  primary backend calls. Values are applied after global
  `primary_request_headers`, so a matched rule can override headers such as
  `Authorization` for one endpoint.
- `shadow_request_headers`: Optional per-rule request header overlay for shadow
  backend calls. Values are applied after global `shadow_request_headers`, so a
  matched rule can override headers such as `Authorization` for one endpoint.

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

#### gRPC Rules

`grpc_rules` is an optional top-level gRPC-only list. Rules are evaluated in
order and the first rule matching the service and method wins. If the list is
empty, global gRPC shadow and comparison settings apply. If the list is
non-empty and no rule matches, the RPC is primary-only and comparison is
skipped.

Rule fields:

- `name`: Optional stable name used in logs. If omitted, a deterministic name
  such as `rule[0]` is used.
- `service`: Required exact service name, for example
  `nauthilus.auth.v1.AuthService`. The special value `*` is a catch-all.
- `methods`: Optional method names without the service prefix. Empty or omitted
  means all methods for the matched service.
- `shadow`: `inherit`, `auto`, `never`, or `always`. `never` blocks force
  metadata; `always` still respects runtime safety such as target availability.
- `compare`: `inherit`, `on`, or `off`.
- `compare_mode`: Optional per-rule mode (`status`, `status_metadata`,
  `message_count`, `message_hash`).
- `compare_metadata`: Optional per-rule metadata/trailer allowlist. If omitted,
  `grpc_compare_metadata` is used. If explicitly `[]`, no metadata keys are
  compared.
- `primary_metadata` / `shadow_metadata`: Per-rule gRPC metadata overlays. They
  are applied independently after incoming metadata is cloned and trace context
  is preserved.

The generic gRPC proxy treats payloads as opaque messages. Unary and streaming
RPCs are forwarded at message level, not by buffering full request or response
bodies. Shadow streaming is best-effort: queue-full, target errors, and shadow
timeouts are logged and observed, but the client always receives the primary
payload and primary status. `message_hash` uses an incremental SHA-256 over the
response-message sequence; descriptor-aware protobuf field diffs are intentionally
out of scope for the current generic mode.

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

## Fake Servers And gRPC Probe

`fakehttpserver` is a standalone program intended to act as primary and shadow HTTP backends for testing. It mirrors incoming request headers into response headers and can optionally randomize selected headers.

### Fake Server Configuration (`fakehttpserver.yaml`)
```yaml
listen_addr: ":9001"
mode: "echo"
random_chance: 10
log_json: true
```

`fakegrpcserver` is a generic gRPC backend for blackbox tests. It uses the same raw message codec as the proxy and supports unary echo, server-stream count, client-stream count, and bidi echo modes. It can set response headers/trailers, return a configured gRPC status, override status or delay from request metadata, and writes JSONL records with the full method, selected metadata excerpt, trace metadata, message counts, and final status.

Example:

```bash
fakegrpcserver \
  -listen 127.0.0.1:9445 \
  -mode auto \
  -response-prefix primary: \
  -header x-backend=primary \
  -trailer x-trailer=primary \
  -log-metadata-key traceparent \
  -log-metadata-key x-trace-id
```

`grpcprobe` sends raw payloads to arbitrary gRPC methods without generated stubs. It supports unary and simple streaming calls, repeated `-metadata key=value`, expected status checks, expected response message counts, expected response messages, and optional header/trailer output.

Example:

```bash
grpcprobe \
  -addr 127.0.0.1:9444 \
  -method /example.Service/Unary \
  -mode unary \
  -payload test \
  -metadata x-shadow=force \
  -expect-status OK \
  -expect-count 1
```

## How It Works

1. **Request Arrival**: The proxy receives an HTTP(S), gRPC, or Milter request.
2. **Primary Request**: The request is forwarded to one primary backend according to the protocol-specific primary target selection mode. The response from that backend is returned to the client.
3. **Shadow Decision**: HTTP uses `path_rules` plus `shadow_force_header`; gRPC uses `grpc_rules` plus `grpc_shadow_force_metadata`; Milter uses the global sampling controls.
4. **Shadow Request**: If selected, the request is mirrored to a shadow backend asynchronously and outside the client response path. HTTP and gRPC per-rule overlays are applied independently to primary and shadow calls.
5. **Comparison**: The proxy compares primary and shadow results using the resolved protocol-specific comparison mode.
6. **Observability & Logging**: A single structured log line is generated containing details about primary and shadow work, durations, selected targets, statuses, comparison outcome, and sanitized diffs. When tracing is active, outgoing HTTP and gRPC primary/shadow requests receive W3C `traceparent` plus the configured bare trace-ID carrier.

### Milter Mode
When `protocol: milter`, the proxy listens on `milter_listen_addr` and forwards incoming Milter frames to the primary backend. It forwards each complete Primary reply sequence, including EOM action frames followed by the terminal disposition, before asynchronously mirroring the request frame to the shadow backend. `SMFIC_MACRO` frames carry state only and have no reply turn, so they are forwarded without waiting for or emitting a response. Shadow frames are serialized per client connection, then their complete reply sequences are compared and logged. A slow, unavailable, or overloaded shadow path stops shadowing only for that connection; it never delays or changes the Primary response, which remains fail-closed.

### gRPC Mode
When `protocol: grpc`, the proxy listens on `grpc_listen_addr` with optional TLS/mTLS and forwards arbitrary gRPC methods through a generic unknown-service handler. Primary headers, trailers, payload messages, and status are client-visible. Shadow headers, trailers, payload counts/hashes, status, queue-full conditions, timeouts, and errors are used for logs, metrics, traces, and comparison only.

## Observability

Observability is disabled by default. When enabled, the proxy records:

- Incoming HTTP requests, gRPC RPCs, and Milter frames with outcome, duration, and shadow-start labels.
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

When the Prometheus server is enabled, `/healthz` reports local proxy readiness on the same listener unless `prometheus_path` itself is `/healthz`. The health endpoint returns `200` only after the configured HTTP, gRPC, or Milter listener has started and returns `503` while starting, failing, or shutting down; Prometheus Basic Auth does not protect `/healthz`.

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

Incoming HTTP and gRPC `traceparent` is extracted and continued. Outgoing HTTP and gRPC requests to Primary and Shadow receive the active W3C trace context and the configured `trace_id_header` value (`x-trace-id` as gRPC metadata). In Milter mode there is no standard header carrier, so the proxy emits spans/metrics for the TCP frame and backend exchanges but does not invent protocol payload fields.

gRPC spans use these names:

- `gRPC /service/Method`
- `gRPC primary /service/Method`
- `gRPC shadow /service/Method`

gRPC metrics reuse the existing metric names with `protocol="grpc"`, full method labels such as `method="/nauthilus.auth.v1.AuthService/Authenticate"`, backend `target="primary"` / `target="shadow"`, and comparison results `same`, `diff`, `skipped`, or `error`.

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
go build -o fakegrpcserver ./cmd/fakegrpcserver
go build -o grpcprobe ./cmd/grpcprobe
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
make e2e-grpc
make e2e-docker
make e2e
```

The E2E checks build local test binaries and run real doppelgaenger processes
against fake Primary/Shadow backends. The HTTP check verifies W3C trace context
and `X-Trace-ID` propagation, Primary/Shadow routing, path rewriting, request
IDs, per-target headers, body limits, OpenMetrics counters, and OTLP
trace/metric export. The gRPC check verifies primary-only and forced-shadow
unary calls, `shadow: never`, primary/shadow metadata overlays, primary-result
preservation on shadow errors, server-stream message counts, OpenMetrics gRPC
series, OTLP gRPC spans, and trace-context propagation to both backends. The
Milter check verifies Primary/Shadow frame forwarding,
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
