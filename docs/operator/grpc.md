# gRPC mode

gRPC mode is a generic, descriptor-free proxy. It forwards unary and streaming
RPCs as opaque protobuf messages, returns the complete Primary stream to the
client, and may run a best-effort Shadow stream for comparison.

## Minimal Primary-only configuration

```yaml
protocol: grpc
grpc_listen_addr: "127.0.0.1:9444"
primary_grpc_targets:
  - name: primary
    address: "127.0.0.1:9445"
    tls:
      enabled: false
shadow_sample_percent: 0
grpc_shadow_force_metadata: ""
grpc_rules: []
```

At least one Primary target is always required. A Shadow target is required
when the effective global settings or any rule can enable Shadow work.

## Listener TLS and message limits

| Key | Default | Meaning |
| --- | --- | --- |
| `grpc_listen_addr` | `:9444` | Inbound gRPC TCP listener. |
| `grpc_tls.enabled` | `false` | Enable inbound TLS. Plaintext mode uses HTTP/2 prior knowledge. |
| `grpc_tls.cert` | empty | Inbound certificate; required with TLS. |
| `grpc_tls.key` | empty | Inbound private key; required with TLS. |
| `grpc_tls.client_ca` | empty | CA used to verify client certificates. |
| `grpc_tls.require_client_cert` | `false` | Require and verify client certificates. Requires TLS and `client_ca`. |
| `grpc_tls.min_tls_version` | `1.2` | `1.2`, `TLS1.2`, `1.3`, or `TLS1.3`. |
| `grpc_max_receive_message_bytes` | `4194304` | Receive limit. Negative values are rejected; zero becomes 4 MiB. |
| `grpc_max_send_message_bytes` | `4194304` | Send limit. Negative values are rejected; zero becomes 4 MiB. |

## Target pools

`primary_grpc_targets` and `shadow_grpc_targets` contain these fields:

| Target field | Meaning |
| --- | --- |
| `name` | Optional stable target label. |
| `address` | Required gRPC target string, normally `host:port` but explicit resolver URIs are also accepted; this is not an HTTP backend URL. |
| `authority` | Optional HTTP/2 `:authority` override. |
| `tls.enabled` | Enable TLS for this target. |
| `tls.root_ca` | Optional custom CA file. |
| `tls.server_name` | Optional certificate server-name override. |
| `tls.insecure_skip_verify` | Disable certificate verification. Unsafe outside controlled testing. |
| `tls.client_cert` | Optional client certificate for mTLS. |
| `tls.client_key` | Optional client key for mTLS. |

Client certificate and key must be configured together and require target TLS.

`primary_grpc_selection_mode` and `shadow_grpc_selection_mode` default to
`round_robin`; the other supported value is `source_ip_hash`. Invalid values
are rejected. Source-IP selection falls back to round robin when the incoming
peer IP is unavailable.

## Shadow runtime

| Key | Default | Meaning |
| --- | --- | --- |
| `grpc_shadow_timeout` | `500ms` | Best-effort Shadow stream lifetime. Non-positive values become `500ms`. |
| `grpc_shadow_queue_size` | `128` | Number of request messages that may wait for Shadow forwarding. Non-positive values become `128`. |
| `grpc_shadow_force_metadata` | empty | Incoming metadata key whose non-empty value forces Shadow unless a rule says `never`. |

The Primary upstream stream does not inherit `grpc_shadow_timeout`. Primary
response messages are forwarded while Shadow runs. After Primary finishes, the
handler waits for Shadow for at most this timeout before returning the final RPC
result, so client-visible completion can be delayed by up to the configured
bound. If the Shadow queue fills, Shadow becomes incomplete with `queue_full`;
Primary request and response streaming continues. Shadow target errors,
timeouts, and non-final streams are comparison errors, not replacements for the
Primary status or messages.

## Rules

`grpc_rules` is ordered and first-match-wins. A rule matches an exact service
name plus an optional method allowlist. `service: "*"` is a catch-all.

| Rule field | Required | Meaning |
| --- | --- | --- |
| `name` | no | Stable log name; otherwise `rule[N]`. |
| `service` | yes | Exact package-qualified service or `*`. |
| `methods` | no | Method names without `/service/`; empty means every method. |
| `shadow` | no | `inherit`, `auto`, `never`, or `always`; default `inherit`. |
| `compare` | no | `inherit`, `on`, or `off`; default `inherit`. |
| `compare_mode` | no | `status`, `status_metadata`, `message_count`, or `message_hash`. |
| `compare_metadata` | no | Metadata/trailer allowlist. Omitted inherits global; `[]` compares none. |
| `primary_metadata` | no | Static metadata overlay for Primary. |
| `shadow_metadata` | no | Static metadata overlay for Shadow. |

With no rules, global sampling and comparison apply. With at least one rule, an
unmatched RPC is Primary-only and comparison is skipped. `shadow: never` blocks
force metadata. `shadow: always` skips sampling but still respects runtime
limits and target availability.

Metadata keys are normalized to lowercase. Overlay keys cannot be pseudo
headers, `content-type`, `te`, `grpc-*`, or binary `*-bin` metadata. Incoming
metadata is otherwise cloned to each upstream before the independent overlay
and trace context are applied.

## Comparison modes

| Key | Default | Meaning |
| --- | --- | --- |
| `grpc_compare_mode` | `status` | Global comparison mode. Invalid values are rejected. |
| `grpc_compare_metadata` | `grpc-status`, `grpc-message` | Allowlist for `status_metadata`. Duplicate or invalid keys are rejected. |

All four modes compare the final gRPC status:

- `status` compares only status.
- `status_metadata` also compares configured keys in response headers and
  trailers. `grpc-status` and `grpc-message` are derived from the final status
  when compared as trailer values.
- `message_count` also compares the number of response messages.
- `message_hash` also compares an incremental SHA-256 hash of the ordered
  response-message sequence. Full messages are not retained for comparison.

Because messages are opaque, `message_hash` reports equality or inequality but
cannot identify protobuf fields.

## Primary OIDC client credentials

`grpc_backend_oidc_auth` can obtain a Bearer token for Primary upstream calls.
It never adds the token to Shadow calls.

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Enable token acquisition. |
| `configuration_uri` | empty | Absolute HTTP(S) OIDC discovery URL. One endpoint source is required. |
| `token_endpoint` | empty | Absolute HTTP(S) token URL. When set, it takes precedence over discovery. |
| `client_id` | empty | Required client ID. |
| `client_secret` | empty | Inline secret; use only one secret source. |
| `client_secret_env` | empty | Environment variable containing the secret. Preferred for deployment. |
| `auth_method` | `auto` | `auto`, `client_secret_basic`, or `client_secret_post`. `auto` currently selects Basic for either supported secret source. |
| `scopes` | `[]` | Space-joined client-credentials scopes. |
| `timeout` | `5s` | Discovery/token request timeout; non-positive values become `5s`. |
| `refresh_skew` | `30s` | Refresh margin before expiry; non-positive values become `30s`. |
| `insecure_tls` | `false` | Disable TLS verification for discovery and token requests. Unsafe outside controlled testing. |

The loader requires exactly one secret source. It rejects
`primary_metadata.authorization` in every rule while dynamic OIDC auth is
enabled. Token acquisition is serialized and cached; a token response without
`expires_in` or with a non-positive value is treated as valid for 60 seconds.
Failure to obtain a token fails that Primary RPC instead of silently sending it
without authorization.
