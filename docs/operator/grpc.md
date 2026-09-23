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
| `ca_file` | empty | PEM CA bundle replacing system roots; empty uses system roots. Must be readable and contain certificates. |
| `server_name` | empty | TLS SNI and certificate hostname override; empty verifies the URL hostname. Does not change the dial address or HTTP Host. |
| `min_tls_version` | `1.2` | Minimum TLS version: `1.2`, `TLS1.2`, `1.3`, or `TLS1.3`. |
| `insecure_tls` | `false` | Disable TLS verification for discovery and token requests. Legacy testing only; emits a warning. Cannot be combined with `ca_file` or `server_name`. |

The loader requires exactly one secret source. It rejects
`primary_metadata.authorization` in every rule while dynamic OIDC auth is
enabled. Token acquisition is serialized and cached; a token response without
`expires_in` or with a non-positive value is treated as valid for 60 seconds.
Failure to obtain a token fails that Primary RPC instead of silently sending it
without authorization.


## Caller authentication

Backend client credentials identify the proxy, not the original caller. Therefore
`grpc_backend_oidc_auth.enabled: true` requires an explicit caller authentication
mode. Invalid or missing configuration fails startup; directly constructed handlers
also fail closed. With backend OIDC disabled and no caller mode selected, existing
forwarding behavior remains unchanged. An explicitly selected mode always applies.

The implementation supports RFC 7662 introspection and mTLS. It does not perform
local JWT Discovery/JWKS verification: JWT and opaque access tokens are checked
by the configured issuer's introspection endpoint. `bearer.go` remains the shared
backend token source; caller verification reuses its Bearer metadata and form
content-type conventions without inheriting its insecure TLS option.

| `grpc_caller_auth` key | Default | Meaning |
| --- | --- | --- |
| `mode` | empty | `introspection` or `mtls`; required with backend OIDC. |
| `allow_unauthenticated` | `false` | Explicit temporary legacy mode; incompatible with a nonempty mode. Emits a startup warning. |
| `introspection_endpoint` | empty | Required HTTPS endpoint for introspection; no redirects, URL credentials, query, or fragment. Certificate verification is mandatory; no insecure TLS option. |
| `ca_file` | empty | PEM CA bundle replacing system roots; empty uses system roots. Must be readable and contain certificates. |
| `server_name` | empty | TLS SNI and certificate hostname override; empty verifies the URL hostname. Does not change the dial address or HTTP Host. |
| `min_tls_version` | `1.2` | Minimum TLS version: `1.2`, `TLS1.2`, `1.3`, or `TLS1.3`. |
| `issuer` | empty | Required exact `iss` value in the introspection result. |
| `audience` | empty | Required expected audience; matches a string or member of an `aud` array. |
| `client_id` | empty | Required proxy client authorized to introspect caller tokens, not the caller's client ID. |
| `client_secret_env` | empty | Required environment variable holding the introspection client's secret; HTTP Basic authentication. |
| `required_scopes` | `[]` | All listed scopes required for every RPC. |
| `method_scopes` | empty | List of `method` (exact `/service/method`) and `scopes` rules. If nonempty, unlisted methods are denied. Method scopes are added to global scopes. |
| `timeout` | `2s` | Per-introspection timeout; zero uses `2s`, negative values are rejected. Together with `introspection_max_concurrent` it bounds how long an unresponsive issuer can block new tokens (see [Overload budget](#overload-budget)). |
| `introspection_cache_ttl` | `30s` | Maximum reuse time for a positive introspection result; `0s` disables the cache, negative values are rejected. Revocation takes effect up to this long after the issuer revokes a token. |
| `introspection_cache_max_entries` | `10000` | Upper bound on cached token digests, at most `1000000`; must be positive while the cache is enabled. Each entry takes at most about 300 bytes with typical claims (10,000 entries: about 3 MiB; the maximum: about 300 MiB). When full, a small random sample is evicted, expired entries first, then the entry expiring soonest. |
| `introspection_max_concurrent` | `64` | Upper bound on introspection requests in progress; zero uses `64`, negative values are rejected. Also sizes the idle keep-alive pool. Requests beyond the limit fail with `Unavailable` (`overload`). |

Introspection requires `active: true`, matching issuer/audience, and a future `exp`.
A supplied future `nbf` is rejected. Missing `iss`, `aud`, or `exp` is rejected even
though RFC 7662 permits optional response fields: configure the issuer to return
these fields. Verification occurs once when a stream opens; existing streams are
not revalidated mid-stream. Exactly one well-formed Bearer metadata value is
accepted. No Primary token fetch, Primary RPC, or Shadow RPC starts for rejected
callers.

Example (replace endpoint, issuer, audience and scopes with the issuer's policy):

```yaml
grpc_caller_auth:
  mode: introspection
  introspection_endpoint: https://127.0.0.1:9443/oauth2/introspect
  ca_file: "" # System roots; or a mounted PEM CA bundle.
  server_name: login.example.net
  min_tls_version: "1.2"
  issuer: https://login.example.net
  audience: proxy-api
  client_id: proxy-introspection
  client_secret_env: DOPPELGAENGER_INTROSPECTION_SECRET
  required_scopes: []
  method_scopes:
    - method: /example.Identity/Authenticate
      scopes: [identity:authenticate]
    - method: /example.Identity/Lookup
      scopes: [identity:lookup]
    - method: /example.Identity/List
      scopes: [identity:list]
  timeout: 2s
  introspection_cache_ttl: 30s # 0s disables caching; revocation lags by up to this value.
  introspection_cache_max_entries: 10000
  introspection_max_concurrent: 64
```

For a pod-local endpoint, keep the loopback URL and set `server_name` to the
certificate's DNS name. Certificate chains and hostnames are still verified.
Apply the same TLS settings independently under `grpc_backend_oidc_auth` and
remove `insecure_tls: true`; they cover both discovery and token requests.
A configured CA bundle replaces, rather than extends, system roots, so it must
trust every HTTPS endpoint used by that client. Mount it read-only and restart
the proxy after changing the bundle. Invalid bundles or TLS versions reject
configuration loading; runtime TLS failures fail authentication closed.
`server_name` is independent of the expected introspection `issuer` claim.

For mTLS, select `grpc_caller_auth.mode: mtls` and set `grpc_tls.enabled: true`,
`cert`, `key`, `client_ca`, and `require_client_cert: true`. The handler additionally
requires a TLS peer with a verified certificate chain. Trust only the intended
caller CA: this mode authorizes every verified certificate from that CA for all
proxied methods and does not apply token scope rules. TLS handshake rejections
happen before RPC metrics. Use TLS for Bearer ingress as well, unless another
trusted transport boundary already provides confidentiality.

RPC rejections emit `grpc caller rejected` with the gRPC status as `reason`, the
bounded `cause` from [Failure classification](#failure-classification), `http_status` for issuer status failures,
and a fixed `detail` for technical failures. Logs never contain the token, claims,
introspection body, or client secret. Caller faults log at `WARN`, technical
failures at `WARN`, configuration failures at `ERROR`, and canceled callers at
`INFO`. Technical causes (`transport`, `timeout`, `http_status`, `response`,
`overload`, `config`) are logged at most once per 10 seconds per cause; the next
record carries the number of suppressed records as `suppressed`. For `config`,
each distinct fixed `detail` has its own interval, so one misconfiguration never
hides another; the remaining causes share one interval per cause because their
details may contain unbounded values such as connection ports. Existing ingress
telemetry counts every rejection in
`doppelgaenger_ingress_requests_total{protocol="grpc",outcome=...}` (and the
equivalent OTel counter) with these outcomes:

| `outcome` | gRPC status |
| --- | --- |
| `caller_auth_rejected` | `Unauthenticated`, `PermissionDenied` |
| `caller_auth_unavailable` | `Unavailable` |
| `caller_auth_canceled` | `Canceled`, `DeadlineExceeded` |

The server span carries the same value as `doppelgaenger.caller_auth.cause`. No
backend request metric is emitted for rejected callers.

`doppelgaenger_grpc_caller_introspections_total{result=...}` counts token lookups:
`hit` (cache), `miss` (own introspection completed), `shared` (joined another
caller's introspection), `error` (technical failure or overload), and `canceled`.
`doppelgaenger_grpc_caller_introspection_flights` shows introspection requests in
progress. With tracing enabled, each request gets a `gRPC caller introspection`
client span (with `doppelgaenger.caller_auth.cause` on failure); server spans carry
`doppelgaenger.caller_auth.introspection` with the lookup result and link to the
introspection span that answered them.

### Failure classification

A rejection caused by the caller is distinguished from a failure of the proxy or
the issuer. Only the former returns `Unauthenticated` or `PermissionDenied`;
technical failures return the retryable `Unavailable`, so callers do not treat a
valid token as invalid while the issuer is overloaded.

| Case | gRPC status | `cause` |
| --- | --- | --- |
| No `authorization` metadata | `Unauthenticated` | `missing_bearer` |
| Duplicate, non-Bearer, malformed, or longer than 8 KiB | `Unauthenticated` | `malformed_bearer` |
| mTLS mode without a verified client chain | `Unauthenticated` | `mtls` |
| `active: false` | `Unauthenticated` | `inactive` |
| Wrong `iss` or `aud`, `exp` reached, or future `nbf` | `Unauthenticated` | `claims` |
| Method not listed in `method_scopes` | `PermissionDenied` | `method` |
| Required scope missing | `PermissionDenied` | `scope` |
| Connection or TLS failure | `Unavailable` | `transport` |
| Introspection exceeded `timeout` | `Unavailable` | `timeout` |
| HTTP 429, 5xx, redirect, 400 without a client error, or any other non-200 status not listed below | `Unavailable` | `http_status` |
| Unparseable or oversized (> 64 KiB) response | `Unavailable` | `response` |
| `introspection_max_concurrent` requests already in progress | `Unavailable` | `overload` |
| Empty client secret, HTTP 400 with RFC 6749 `error` `invalid_client` or `unauthorized_client`, HTTP 401/403, invalid configuration or CA bundle | `Unavailable` | `config` |
| Caller canceled while waiting | `Canceled` | `canceled` |
| Caller deadline expired while waiting | `DeadlineExceeded` | `deadline` |

Proxy misconfiguration returns `Unavailable` rather than `Internal`: the caller is
not at fault, the RPC succeeds once the operator fixes the configuration, and the
proxy reports other missing dependencies (Primary token, Primary target) the same
way. These cases are logged at `ERROR` with `cause=config`; fix them instead of
relying on caller retries. Callers retrying `Unavailable` add load to an already
struggling issuer, so use bounded retries with backoff.

A plain HTTP 400 is not treated as misconfiguration because token content chosen
by the caller can trigger it.

Only the Bearer value is length-limited (8 KiB). The inbound gRPC header list
keeps the gRPC default limit (16 MiB): the proxy forwards arbitrary caller
metadata to backends, so a lower fixed limit could break existing callers
without warning.

### Overload budget

At most `introspection_max_concurrent` introspection requests run at once, and
each is abandoned after `timeout`. If the issuer stops responding, the limit is
reached within one `timeout`; until requests time out, RPCs with uncached tokens
fail immediately with `Unavailable` (`overload`), while cached tokens and tokens
already being introspected keep working. The worst-case lock-out for a new token
is therefore about one `timeout`, and a hung issuer receives at most
`introspection_max_concurrent / timeout` new requests per second (defaults:
64 / 2s = 32/s). Lower `timeout` to shorten the lock-out; raise the limit only
if the issuer can take the additional concurrent load.

### Positive introspection cache

Positive results are cached in memory to reduce issuer load:

- Only responses with `active: true` and a valid issuer, audience, `exp`, and `nbf`
  are cached. Inactive tokens, claim failures, and every technical failure are
  never cached, so the next RPC introspects again.
- The cache key is the SHA-256 digest of the token. Raw tokens are not retained
  after the introspection request completes.
- An entry lives for `min(introspection_cache_ttl, exp - now)`. `exp` and `nbf`
  are checked again on every cache hit, and method/scope rules are evaluated for
  every RPC against the cached claims.
- Concurrent RPCs with the same uncached token share one introspection request
  (also with the cache disabled). A caller that cancels while waiting receives
  `Canceled` without aborting the shared request for the others; the request is
  canceled once no caller waits for it anymore.
- Negative results (`active: false`) are deliberately not cached. Token values
  are chosen by unauthenticated callers, so negative entries would let anyone
  fill the cache and evict valid entries; concurrent duplicates are already
  collapsed, and `introspection_max_concurrent` bounds the issuer load an
  invalid-token flood can cause.
- Security trade-off: token revocation at the issuer takes effect after up to
  `introspection_cache_ttl`. Lower the TTL, or set `0s`, when faster revocation
  matters more than issuer load. Each proxy process has its own cache.
- Upgrades enable the cache with the `30s` default. Set
  `introspection_cache_ttl: 0s` to keep introspecting every RPC.

### Migration

Upgrading enables the positive introspection cache with `introspection_cache_ttl:
30s`: a token revoked at the issuer keeps working for up to 30 seconds. Set `0s`
before upgrading if that delay is unacceptable. Issuer failures now return
`Unavailable` instead of `Unauthenticated`; make sure callers retry `Unavailable`
with bounded backoff. The default introspection `timeout` drops from `5s` to `2s`
to shorten the [overload budget](#overload-budget); keep an explicit `timeout: 5s`
if the issuer regularly needs longer.

Update the deployed configuration and supply the introspection secret through a
Kubernetes Secret before upgrading the image. The introspection client must be
permitted by the issuer to inspect tokens issued to all intended caller clients.
Verify valid callers on each allowed method and reject missing tokens and scopes.
The Primary still receives the proxy's backend token; Shadow behavior remains as
configured after caller verification.

If migration cannot be atomic, temporarily set only
`grpc_caller_auth.allow_unauthenticated: true` (leave `mode` empty). This deliberately
restores the old vulnerability: reachable callers can use backend service privileges.
A startup warning makes this visible. Remove the switch as soon as caller validation
is configured. Never silently fall back when introspection is unavailable.
