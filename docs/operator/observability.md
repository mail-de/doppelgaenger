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
| `doppelgaenger_backend_retries_total` | `protocol`, `target`, `result` (`retry`, `failover`, `exhausted`) |
| `doppelgaenger_comparisons_total` | `protocol`, `result` |
| `doppelgaenger_observability_startups_total` | `result` |
| `doppelgaenger_observability_shutdowns_total` | `result` |
| `doppelgaenger_grpc_caller_introspections_total` | `result` (`hit`, `miss`, `shared`, `error`, `canceled`) |
| `doppelgaenger_grpc_caller_introspection_flights` | none (gauge) |

The two duration series are histograms. `method` is an HTTP method, a full gRPC
method, or a one-byte Milter command depending on `protocol`. Backend `target`
is `primary` or `shadow`, not a configured host name.

The retry counter only moves when `primary_failover` or `shadow_failover` is
enabled; see [Retry and failover](failover.md#observability).

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
With retry or failover enabled, HTTP and gRPC result logs add
`primary_attempts`, `primary_failover_from`, `shadow_attempts`, and
`shadow_failover_from` when more than one attempt was made, and each repeated
attempt is logged as `backend_attempt_failed`.

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
credentials, cookies, and other values that should not enter logs. Milter uses
its own macro allowlist, described below.

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
Normal result log filters still apply. For gRPC message fields, use the
protobuf scalar selections described below.


### gRPC protobuf scalars

For backends returning correlation values inside protobuf messages rather than
metadata, select field-number paths from the backend's `.proto` schema:

```yaml
log_fields:
  grpc_messages:
    - method: /example.Auth/Authenticate
      request:
        - {name: auth_user, path: [1], type: string}
        - {name: client_ip, path: [4, 2], type: string}
      response:
        - {name: session_id, path: [3], type: string}
```

Methods match exactly. Paths use positive protobuf field numbers, including
nested singular messages. Explicit types are `string`, `bool`, `int64`, `uint64`,
and `sint64`; enums can use `int64`. No generated backend code, reflection,
descriptor files, or backend-specific defaults are required. Keep selections
aligned with your backend schema: wire field numbers do not encode field names
or distinguish a string from an opaque byte value.

Only the first message in each direction is captured, before forwarding overlays.
Values appear in `request_protobuf`, `primary_response_protobuf`, and
`shadow_response_protobuf`, independently of status/payload comparison. Each
configured direction also has a `<group>_status` of `not_seen`, `captured`,
`no_fields`, `malformed`, or `message_too_large`. A missing response, skipped
shadow, or message without a selected field does not fabricate a session ID.
Missing proto3 default values are omitted. Selectors must target singular fields. Multiple wire occurrences of selected
fields or ancestors are ambiguous and omitted. Without descriptors, a single
occurrence of a repeated field or map cannot be distinguished from a singular
field; do not configure such paths. Packed fields, byte dumps, and legacy
protobuf groups are unsupported. Invalid framing suppresses capture
without changing forwarded bytes or RPC status.

Limits: 128 exact methods, 32 fields per direction, path depth 8, inspected
message size 64 KiB, UTF-8 string size 4 KiB, and selected values 16 KiB per
direction. Oversized values are omitted, never truncated. Only selected scalar
copies survive capture; request/response payloads are not retained for logging.
Existing result log levels and `log_only_on_diff` still apply. Use
`log_level: info` and `log_only_on_diff: false` to see successful RPC results.

### Milter macros

Select macros supplied by the connecting MTA:

```yaml
log_fields:
  milter_macros:
    - {name: queue_id, macro: i}
    - {name: auth_user, macro: "{auth_authen}"}
    - {name: client_ip, macro: "{client_addr}"}
    - {name: client_port, macro: "{client_port}"}
```

Names may be written with or without braces. Values appear under
`milter_macros` using the normalized alias. Only macros actually sent by the MTA
are available; configure the MTA's macro lists separately. Doppelgaenger does
not request additional macros or alter negotiation/forwarding. A queue ID is
an MTA correlation value, not an authentication backend's session ID.

`milter_connection_id` and `milter_message_number` accompany result events when
macro capture is configured. The connection ID uses the process-local request
counter; combine it with pod/container identity across replicas or restarts.
The message number increases on MAIL FROM and survives connection reuse.
Connection and HELO macros remain active across messages. MAIL FROM starts a
fresh message scope; RCPT, DATA, end-of-headers, and end-of-message stages replace their
own values even when no new macros arrive. End-of-message/abort clears message
values after taking the log snapshot; QUIT/QUIT_NC clears all scopes. Earlier
asynchronous comparisons keep their own immutable snapshot. Stage precedence
is CONNECT, HELO, MAIL, RCPT, DATA, end-of-headers, end-of-message.

Macro frames must contain complete NUL-terminated name/value pairs. Malformed
frames clear capture for their stage and remain forwarded unchanged. Limits:
32 selections, macro/alias names 64 ASCII characters, macro frame 64 KiB,
UTF-8 value 4 KiB, selected values 16 KiB per stage and result snapshot.
Missing/empty/oversized values are omitted. No passwords or arbitrary macro
values are logged by default. Milter result events use the normal result log
level (`info` for successful matches); `log_only_on_diff` currently applies only
to HTTP and gRPC.
