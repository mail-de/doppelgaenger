# HTTP mode

HTTP mode proxies each request to one Primary HTTP backend, returns that
response, and may run a detached request against one Shadow backend.

## Minimal configuration

```yaml
protocol: http
listen_addr: "127.0.0.1:8080"
primary_base_urls:
  - "http://127.0.0.1:9001"
shadow_base_urls:
  - "http://127.0.0.1:9002"
shadow_sample_percent: 0
log_json: true
```

Both backend lists are required in HTTP mode, even when the sample percentage
is zero.

## Listener and backend settings

| Key | Default | Meaning |
| --- | --- | --- |
| `listen_addr` | `:8080` | Inbound HTTP listener. |
| `tls_cert_file` | empty | Inbound certificate. Must be paired with `tls_key_file`. |
| `tls_key_file` | empty | Inbound private key. Must be paired with `tls_cert_file`. |
| `primary_base_urls` | `https://127.0.0.1:9001` | Required Primary URL pool. |
| `shadow_base_urls` | `https://127.0.0.1:9002` | Required Shadow URL pool. |
| `primary_selection_mode` | `round_robin` | `round_robin` or `source_ip_hash`. Unknown values currently normalize to `round_robin`. |
| `shadow_selection_mode` | `round_robin` | `round_robin` or `source_ip_hash`. Unknown values currently normalize to `round_robin`. |

With both inbound TLS files set, the listener supports HTTP/1.1 and HTTP/2 via
ALPN. Without them, it serves plaintext HTTP/1.1. An incomplete pair is a
startup error.

`source_ip_hash` removes the client port before hashing. If no usable client IP
is present, selection falls back to the selector's normal behavior.

## Upstream transport

| Key | Default | Meaning |
| --- | --- | --- |
| `root_ca` | empty | Common CA file for both backend pools. |
| `primary_root_ca` | empty | Primary-specific CA; overrides `root_ca`. |
| `shadow_root_ca` | empty | Shadow-specific CA; overrides `root_ca`. |
| `insecure_upstream` | `false` | Disable TLS certificate verification for both pools. Unsafe outside controlled testing. |
| `upstream_http_dial_timeout` | `2s` | TCP connect timeout; non-positive values become `2s`. |
| `upstream_http_tls_handshake_timeout` | `5s` | TLS handshake timeout; non-positive values become `5s`. |
| `upstream_http_response_header_timeout` | `5s` | Time to response headers; non-positive values become `5s`. |
| `upstream_http_max_idle_conns` | `1024` | Idle connections across all upstreams; non-positive values become `1024`. |
| `upstream_http_max_idle_conns_per_host` | `256` | Idle connections per upstream; non-positive values become `256`. |
| `upstream_http_max_conns_per_host` | `0` | Total connections per host; `0` is unlimited and negative values become `0`. |
| `upstream_http_protocol` | `auto` | `auto`, `http1`, or `http2`. Invalid values are rejected. |

`auto` allows HTTP/2 negotiation when offered. `http1` disables HTTP/2.
`http2` rejects a response if the backend negotiated HTTP/1.1. Backend
redirects are returned to the caller; Doppelgaenger does not follow them.

## Request and response handling

| Key | Default | Meaning |
| --- | --- | --- |
| `max_backend_body_bytes` | `32768` | Shared request and response-body buffer limit. A positive limit returns HTTP 413 for a larger request and truncates each backend response body to the limit; `0` removes this explicit cap. |
| `primary_request_headers` | `{}` | Static header overlay for Primary requests. |
| `shadow_request_headers` | `{}` | Static header overlay for Shadow requests. |
| `forward_response_headers` | annotated list | Primary response headers copied to the client. |
| `shadow_timeout` | `150ms` | Detached Shadow request lifetime. |
| `shadow_force_header` | `X-Shadow` | A non-empty inbound value forces Shadow unless a rule says `never`. Empty disables forcing. |

Matched rule overlays are applied after global request overlays. Hop-by-hop
headers are removed before upstream forwarding. Doppelgaenger preserves the
incoming `Host`, adds standard forwarding headers, and preserves or generates
`X-Request-ID`.

HTTP request and response bodies are buffered rather than streamed. The same
`max_backend_body_bytes` value is used in both directions. Backend response
truncation is not currently reported as an error, so size this field above every
expected client-visible Primary response or use zero only after considering the
memory impact.

Only configured response headers are forwarded normally. For HTTP 3xx responses,
`Location` and `Set-Cookie` are additionally preserved so redirects remain
client-visible.

If a Primary session cannot be created, the client receives 503. A Primary
request or response error produces 502. A Shadow failure changes neither a
successful Primary status nor body.

## Path rules

`path_rules` is an ordered list. Rules match the inbound path before path
rewriting; query strings are not part of the match. The first rule matching
both `methods` and the `match` regular expression wins.

| Rule field | Required | Meaning |
| --- | --- | --- |
| `name` | no | Stable log name; otherwise `rule[N]`. |
| `methods` | no | HTTP method allowlist. Empty means all methods. |
| `match` | yes | Go regular expression for the inbound path. |
| `shadow` | no | `inherit`, `auto`, `never`, or `always`; default `inherit`. |
| `compare` | no | `inherit`, `on`, or `off`; default `inherit`. |
| `compare_mode` | no | `header`, `json`, or `html`. |
| `compare_headers` | no | Per-rule response header allowlist. Omitted inherits global; `[]` compares none. |
| `primary_request_headers` | no | Per-rule Primary request overlay. |
| `shadow_request_headers` | no | Per-rule Shadow request overlay. |

When `path_rules` is empty, global sampling and comparison apply. When it is
non-empty and no rule matches, the request is Primary-only and comparison is
skipped. Add an explicit catch-all if that is not the desired unmatched policy.

`shadow: auto` and `inherit` use sampling and the force header. `always` skips
sampling but still uses the rate limiter. `never` overrides the force header.
Comparison only runs after Shadow work starts.

## Path mapping

`path_mapping.mode` is `direct` by default. `rewrite` evaluates
`path_mapping.rules` in order and applies the first matching regular expression.

```yaml
path_mapping:
  mode: rewrite
  rules:
    - match: "^/api/v1/(.*)$"
      primary: "/v1/$1"
      shadow: "/legacy/$1"
```

Each rule has `match`, `primary`, and `shadow`. `match` is required. An empty
replacement keeps the original path for that backend. No match also keeps the
original path.

## Comparison

| Key | Default | Meaning |
| --- | --- | --- |
| `compare_mode` | `header` | `header`, `json`, or `html`. Unknown global values currently normalize to `header`. |
| `compare_headers` | annotated list | Response header allowlist used by every HTTP comparator unless a rule overrides it. |
| `compare_json_strict` | `false` | Strict mode compares compact JSON bytes. Non-strict mode compares decoded values and ignores object key order, but not array order. |
| `compare_html_threshold` | `0.99` | Minimum Jaccard similarity of visible text tokens; clamped to 0 through 1. |

`header` compares only selected response headers. `json` compares those headers
and the body. `html` compares those headers and visible HTML text, excluding
`script`, `style`, and `noscript` content.

In non-strict JSON mode, a missing object member and an explicit JSON `null`
currently both become a nil decoded value and compare as equal. HTML similarity
uses sets of normalized text tokens, so repeated occurrences do not add weight.

Primary and Shadow HTTP status codes are logged separately, but status-code
equality is not part of the current HTTP `diff` calculation. Alert directly on
the logged status fields if status parity is required.
