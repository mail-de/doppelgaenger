# Observability

Observability is opt-in. Prometheus/OpenMetrics and OpenTelemetry are separate
outputs and may be enabled independently. Structured result logging remains
available, subject to the documented log-suppression settings.

## Readiness and Prometheus

The Prometheus server is also the only HTTP server that exposes local
readiness. If `prometheus_enabled` is false, Doppelgaenger does not expose
`/healthz`.

```yaml
observability:
  prometheus_enabled: true
  prometheus_address: "127.0.0.1"
  prometheus_port: 9464
  prometheus_path: "/metrics"
  prometheus_runtime_metrics: false
```

| Field | Default | Meaning |
| --- | --- | --- |
| `prometheus_enabled` | `false` | Start the dedicated HTTP listener. |
| `prometheus_address` | `127.0.0.1` | Bind address; must be non-empty when enabled. |
| `prometheus_port` | `9464` | Bind port, validated from 1 through 65535. |
| `prometheus_path` | `/metrics` | Metrics path; must start with `/`. |
| `prometheus_runtime_metrics` | `false` | Add Go and process collectors. |
| `prometheus_http_auth_basic` | empty | Metrics credentials in `user:password` form. |
| `prometheus_tls.enabled` | `false` | Enable server-side TLS. |
| `prometheus_tls.cert` | empty | Certificate required with TLS. |
| `prometheus_tls.key` | empty | Private key required with TLS. |
| `prometheus_tls.min_tls_version` | `1.2` | TLS 1.2 or 1.3 syntax accepted by the main config validator. |

With the normal metrics path, `GET /healthz` and `HEAD /healthz` return 200
only after the selected protocol listener is ready. The JSON body contains
`status`, `protocol`, `ready`, and `shutting_down`. Other methods return 405.

Readiness is local only. It does not connect to Primary, Shadow, OIDC, or OTLP.
Basic authentication protects the metrics path but deliberately does not
protect the readiness path.

Do not set `prometheus_path: /healthz` if readiness is required. In that special
case the path serves metrics instead and no separate readiness handler is
registered.

## Prometheus metrics

| Metric | Labels |
| --- | --- |
| `doppelgaenger_ingress_requests_total` | `protocol`, `method`, `outcome`, `shadow`, `shadow_started` |
| `doppelgaenger_ingress_request_duration_seconds` | the same labels |
| `doppelgaenger_backend_requests_total` | `protocol`, `target`, `method`, `status`, `result` |
| `doppelgaenger_backend_request_duration_seconds` | the same labels |
| `doppelgaenger_comparisons_total` | `protocol`, `result` |
| `doppelgaenger_observability_startups_total` | `result` |
| `doppelgaenger_observability_shutdowns_total` | `result` |
| `doppelgaenger_grpc_caller_introspections_total` | `result` (`hit`, `miss`, `shared`, `error`, `canceled`) |
| `doppelgaenger_grpc_caller_introspection_flights` | none (gauge) |

The two duration series are histograms. `method` is an HTTP method, a full gRPC
method, or a one-byte Milter command depending on `protocol`. Backend `target`
is `primary` or `shadow`, not a configured host name.

Comparison results use `same`, `diff`, `error`, or `skipped`. Ingress outcomes
include `ok`, `bad_request`, `primary_error`, `timeout`, and `error` where the
protocol path can produce them. gRPC caller authentication adds
`caller_auth_rejected`, `caller_auth_unavailable`, and `caller_auth_canceled`; see
[Caller authentication](grpc.md#caller-authentication).

## OpenTelemetry

```yaml
observability:
  otel_enabled: true
  otel_traces_enabled: true
  otel_metrics_enabled: true
  otel_service_name: "doppelgaenger"
  otel_service_version: ""
  otel_exporter_otlp_endpoint: "http://127.0.0.1:4318"
  otel_exporter_otlp_headers: {}
  otel_exporter_otlp_insecure: true
  otel_sample_ratio: 1.0
  trace_id_header: "X-Trace-ID"
```

| Field | Default | Meaning |
| --- | --- | --- |
| `otel_enabled` | `false` | Master exporter switch. Requires traces or metrics plus an endpoint. |
| `otel_traces_enabled` | `false` | Export OTLP/HTTP traces. |
| `otel_metrics_enabled` | `false` | Export OTLP/HTTP metrics. |
| `otel_service_name` | `doppelgaenger` | Resource `service.name`. |
| `otel_service_version` | binary version | Resource `service.version`; empty uses the injected build version. |
| `otel_exporter_otlp_endpoint` | empty | OTLP/HTTP collector endpoint. Required when enabled. |
| `otel_exporter_otlp_headers` | `{}` | Headers sent to the collector. Treat values as secrets when applicable. |
| `otel_exporter_otlp_insecure` | `false` | Use an insecure HTTP transport. An endpoint with `http://` also selects it. |
| `otel_sample_ratio` | `1.0` | Parent-based ratio from 0 through 1. |
| `trace_id_header` | `X-Trace-ID` | Bare trace ID carrier added to HTTP and gRPC upstream calls. An empty value is normalized back to the default. |

The OTLP metric instrument names omit Prometheus suffixes:
`doppelgaenger_ingress_requests`, `doppelgaenger_ingress_request_duration`,
`doppelgaenger_backend_requests`, `doppelgaenger_backend_request_duration`, and
`doppelgaenger_comparisons`. Duration units are seconds.

HTTP and gRPC extract W3C `traceparent` from incoming traffic and inject the
active context into both upstreams. The configured bare trace ID is added as an
HTTP header or lowercase gRPC metadata. This propagation also continues an
incoming context when local OTLP export is disabled. Without an incoming trace
and without local tracing, no new trace ID is invented.

Milter has no standard trace carrier. It produces local spans and metrics when
enabled but does not modify Milter frames to carry trace data.

Representative span names are:

- `HTTP GET`, `HTTP GET primary`, and `HTTP GET shadow`
- `gRPC /package.Service/Method`, `gRPC primary /package.Service/Method`, and
  `gRPC shadow /package.Service/Method`
- `milter E`, `milter primary E`, `milter shadow E`, and Milter connect spans

## Result logs

The stable event values are:

- `auth_proxy` for completed HTTP result logging
- `grpc_proxy` for completed gRPC result logging
- `milter_proxy` for each processed Milter frame

Each includes request identity, protocol-specific routing, Primary and Shadow
outcomes, comparison state, and errors. HTTP logs expose `primary_status` and
`shadow_status`; gRPC logs expose status, message count, message hash, and the
comparison outcome; Milter logs expose terminal decisions and raw differences.

Startup, reload, listener, and failure messages are separate lifecycle logs.
Build alerts on explicit fields and metric labels rather than matching free-form
message text where a structured event exists.

## Log severity

Set `log_level: warn` in the YAML configuration for high-traffic deployments.
The default is `info`; accepted levels, in order, are `debug`, `info`, `notice`,
`warn`, and `error`. `none` disables all application logger output. Level names
are case-insensitive; invalid values reject configuration loading. Restart the
process or use the existing Unix SIGHUP re-exec reload to apply changes.

Clean request results use `info`. Comparison differences use `notice`; Shadow
and comparison failures use `warn`, and primary transport failures use `error`.
Startup, listener, runtime security, and reload events use `notice`. Existing
warning and error events keep their severities. `notice` is rendered as `NOTICE`
in both JSON and text output. A threshold includes all higher severities.

`log_only_on_diff` additionally filters HTTP and gRPC result events; it cannot
override the severity threshold. Metrics and traces remain active independently
of logging, including at `none`. CLI diagnostics and third-party output that
bypasses the application logger are outside this setting. The standalone test
servers retain their own logging configuration.


## Correlation fields

`trace_id` is omitted when no valid trace is active, for example when tracing
is disabled. `req_id` remains available; HTTP also logs `x_request_id`.

Use explicit allowlists to include incoming HTTP headers or gRPC metadata:

```yaml
log_fields:
  request_headers: [Auth-User, X-Client-ID]
  response_headers: [X-Session-ID]
  request_metadata: [x-client-id]
  response_metadata: [x-session-id]
```

All header and metadata lists default to empty. Choose response header names returned by your
backend to correlate its logs with each comparison. The values appear in
`request_headers`, `primary_response_headers`, and `shadow_response_headers`.
For example, a selected `X-Session-ID` becomes
`primary_response_headers.x_session_id` in JSON. gRPC uses `request_metadata`,
`primary_response_metadata`, `shadow_response_metadata`,
`primary_trailer_metadata`, and `shadow_trailer_metadata`.

Field names become lowercase; punctuation becomes underscores. Invalid names
and duplicate normalized keys in the same allowlist reject configuration.
gRPC names must be lowercase ASCII metadata keys; binary (`-bin`) values are
not supported. Missing or empty values and empty groups are omitted. One value
is logged as a string; repeated values are logged as an ordered array.

Incoming values are captured before forwarding headers or backend-specific
overlays are applied. Only allowlisted fields are added; avoid passwords,
credentials, cookies, and other values that should not enter logs. Milter has
no corresponding header or metadata capture.

Response correlation fields are read from the original backend response,
independently of `compare_headers`, response forwarding, and
`log_session_only_on_diff`. They do not affect comparison results. Normal
`log_level` and `log_only_on_diff` filters still apply to the entire result
event. Restart or reload the process to apply the configuration.


### HTTP JSON bodies

Use `request_json` and `response_json` maps to select JSON body fields:

```yaml
log_fields:
  request_json:
    auth_user: "$.username"
    client_id: "$.client.id"
  response_json:
    session_id: "$.session.id"
```

Each entry maps a log field alias to a case-sensitive JSON selector. Aliases
are normalized like header names; duplicate normalized aliases are rejected,
including case-only collisions. Alias spelling is preserved when loading YAML.

The supported JSONPath subset selects exactly one location: `$.client.id`,
`$.items[0].id`, or quoted members such as `$['client.id']` and `$["client-id"]`.
Dot members support ASCII letters, digits after the first character, and
underscores. Use quoted brackets for punctuation or Unicode property names.
Bracket members accept JSON string escapes and single-quoted strings also
accept `\'`. Array indices must be canonical non-negative integers. Root-only
selectors, wildcards, filters, slices, unions, and recursive descent are
rejected when loading configuration.

Non-root RFC 6901 JSON pointers are also supported: `/client/id` selects a
nested property, `/items/0/id` selects the first array item's property, and
`~1` and `~0` escape `/` and `~`. Invalid pointer syntax rejects configuration.

The maps default to empty. JSON parsing is opt-in and independent of comparison
mode and Content-Type. Bodies must contain exactly one valid JSON document.
Missing paths, malformed JSON, null, empty strings, objects, and arrays are
omitted. Select individual scalar leaves explicitly; strings, booleans, and
numbers are supported. Large numbers retain their precision. No whole bodies
or JSON parser errors containing body data are logged by this feature.

Values appear under `request_json`, `primary_response_json`, and
`shadow_response_json`, for example `request_json.auth_user`. Logging leaves
request and response bodies unchanged and uses the existing body size limits.
Normal result log filters still apply. This supports HTTP JSON bodies; gRPC
payloads remain opaque and use the metadata allowlists described above.
