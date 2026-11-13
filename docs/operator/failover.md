# Retry and failover

Doppelgaenger can repeat a backend attempt on the same target (retry) or move
it to another target of the same pool (failover) when the selected Primary or
Shadow backend cannot be reached. The main use case is a rolling upgrade of a
backend pool: while one backend restarts, requests that selected it are served
by the remaining backends instead of failing.

The feature is available in HTTP and gRPC mode, for Primary and Shadow pools
independently. It is disabled by default; with `enabled: false` the proxy makes
exactly one attempt, as before.

## Safety rule: only connect-phase failures are repeated

An attempt is repeated only when the request provably never reached a backend.
This makes the feature safe for non-idempotent requests such as HTTP `POST` or
any gRPC method: nothing can be executed twice.

| Protocol | Repeated | Never repeated |
| --- | --- | --- |
| HTTP | No connection was obtained: connection refused, dial timeout, DNS failure, no route, TLS handshake failure or timeout, proxy `CONNECT` failure. | Any HTTP status, including `502`, `503`, and `504`; response-header timeout; connection reset or EOF after a connection was obtained; HTTP/2 stream resets; an `upstream_http_protocol: http2` mismatch; local request errors such as an invalid header. |
| gRPC | Stream creation fails with `UNAVAILABLE` (for example connection refused or a channel in transient failure), or stream creation exceeds `attempt_timeout`. | Every failure after the stream exists, including `UNAVAILABLE` returned by the backend handler; all other status codes; a closed channel. |

The client request context always wins. When the client disconnects, its
deadline expires, or the Shadow timeout ends, no further attempt starts.

Backends that are taken down must therefore shut down gracefully: stop
accepting new connections, finish in-flight requests, then exit. Requests that
are already running on a backend that is killed hard still fail; they are not
repeated, because the proxy cannot know whether the backend acted on them.

Keep-alive connections need one more rule. When a reused HTTP connection
fails, the Go HTTP transport retries the request on a new connection to the
same backend by itself: for any request when nothing was written yet, and for
requests it considers replayable (GET, HEAD, OPTIONS, TRACE, or an
`Idempotency-Key` or `X-Idempotency-Key` header) even after the request was
sent. If that new connection is refused:

- a non-replayable request (for example a plain `POST`) fails over, because the
  transport only retries it when the backend provably did not process it:
  nothing was written (HTTP/1.1), or the backend refused the stream with
  `REFUSED_STREAM` or a `GOAWAY` that excludes it (HTTP/2);
- a replayable request does not fail over, because the backend may already
  have received it. It fails with `502` like without failover.

This race only affects requests that pick an idle connection in the moment the
backend closes it. A backend that shuts down gracefully closes idle
connections first, so most requests get a fresh connection and fail over
normally. A `POST` that was written onto a connection just as the backend
closed it also fails with `502`; it is never repeated.

Graceful gRPC shutdown (`GOAWAY`) is handled by the gRPC client's own safe
retry logic; when the replacement connection is then refused, failover takes
over.

## Configuration

```yaml
primary_failover:
  enabled: false
  max_targets: 0
  retries: 0
  backoff_initial: "100ms"
  backoff_multiplier: 2.0
  backoff_max: "2s"
  backoff_jitter: 0.2
  max_wait: "5s"
  cooldown: "10s"
  attempt_timeout: "2s"

shadow_failover:
  enabled: false
  # Same keys and defaults as primary_failover.
```

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Turns retry and failover on for the pool. `false` keeps the single-attempt behavior. |
| `max_targets` | `0` | Distinct targets tried per round. `0` means all targets in the pool, `1` disables failover and only retries the selected target. |
| `retries` | `0` | Extra rounds after the first one, `0` to `20`. Each extra round waits for the backoff first. |
| `backoff_initial` | `100ms` | Delay before the first extra round. Must be positive. |
| `backoff_multiplier` | `2.0` | Growth factor per round. Must be at least `1`. |
| `backoff_max` | `2s` | Upper bound for a single delay. Must be at least `backoff_initial`. |
| `backoff_jitter` | `0.2` | Randomizes each delay by up to plus or minus this fraction, `0` to `1`. The result never exceeds `backoff_max`. |
| `max_wait` | `5s` | Total budget measured from the first attempt. No new attempt or backoff starts once it would be exceeded. `0` removes this budget; the attempt count and the request deadline still apply. |
| `cooldown` | `10s` | After a connect failure the target is tried last for this long. `0` disables the cooldown. The `least_conn` selection mode needs a value above `0` and applies it even while `enabled` is `false`; see [Backend selection](selection.md#unreachable-targets). |
| `attempt_timeout` | `2s` | gRPC only: the longest time one stream creation may wait, for example for a host that accepts TCP but never answers. It does not limit the stream once it exists. If it expires in the instant the stream was created, the call fails with `UNAVAILABLE` and is not repeated. HTTP uses the pool-specific dial timeout (falling back to `upstream_http_dial_timeout`) and `upstream_http_tls_handshake_timeout` instead. |

All values are validated at startup, even when `enabled` is `false`. Durations
must not exceed one hour. Enabling
failover with `protocol: milter` is a startup error; see
[Milter](#milter).

### Attempts, rounds, and backoff

One round tries up to `max_targets` distinct targets back to back without a
delay (failover). If every target in the round failed and `retries` allows
another round, the proxy waits and starts the next round (retry). The delay
before extra round `n` (starting at zero) is:

```text
delay(n) = min(backoff_initial * backoff_multiplier^n, backoff_max) +/- jitter
```

The attempt count therefore is at most `max_targets * (retries + 1)`, bounded
earlier by `max_wait` and the request deadline.

| Goal | Settings |
| --- | --- |
| Failover only: try every other target once | `enabled: true`, `retries: 0` |
| Retry only: wait for the selected target | `enabled: true`, `max_targets: 1`, `retries: 3` |
| Both: try all targets, then retry the pool | `enabled: true`, `retries: 2` |

### Target order

The first attempt always uses the target chosen by the configured selection
mode, so traffic distribution is unchanged while all targets are healthy.

- `round_robin`: fallbacks follow the configured list order after the selected
  target, wrapping around.
- `source_ip_hash`: the first choice is the same hash result as without
  failover. Fallbacks are a stable, per-client permutation of the remaining
  targets. Clients of a failed target are spread over all surviving targets,
  each client always lands on the same fallback, and clients of healthy targets
  are not moved. After the failed target is back (and its cooldown expired),
  its clients return to it.
- `least_conn`: the first choice is the target with the fewest requests in
  flight that is not in cooldown. Fallbacks follow with the least busy target
  first. A request counts on the target of its current attempt only, so a
  failed attempt does not leave load behind.

Use `max_targets: 1` with `source_ip_hash` when affinity must never be broken;
the proxy then only retries the hashed target.

### Cooldown

The cooldown is passive and process local. A target whose connect attempt
failed is moved to the end of the candidate order for `cooldown`, so later
requests do not each pay a connect timeout for a target that is known to be
down. It is fail-open: a target in cooldown is still tried when no other
target is left, and when every target is in cooldown the normal order is used.
A successful connection to a target clears its cooldown immediately. A
`least_conn` pool is stricter and keeps the target out for the whole cooldown;
see [Backend selection](selection.md#unreachable-targets).

With `round_robin`, requests that selected a target in cooldown go to the next
target in the list, so that target carries the extra load while its neighbour
is down. Plan capacity per backend accordingly, or use `least_conn`: it does
not select a target in cooldown in the first place and spreads the traffic
over all remaining targets by their current load.

In gRPC mode, targets whose channel reports transient failure are also ordered
last. Each gRPC channel reconnects with the gRPC client's own backoff, which
starts at about one second and grows up to two minutes. Until a channel has
reconnected, stream creation on it fails immediately. Retrying the same target
(`max_targets: 1`, or a pool with one target) therefore only succeeds once the
channel reconnected within `max_wait`; failover to other targets does not
depend on it.

### Worst-case latency

Without failures, failover adds no delay. With failures, the extra latency of
one request is bounded by `max_wait` plus the connect time of the attempts that
were already running when the budget was reached. A connect attempt takes up to
the effective pool-specific HTTP dial timeout plus `upstream_http_tls_handshake_timeout` in HTTP
mode and up to `attempt_timeout` in gRPC mode. Refused connections fail within
milliseconds; blackholed hosts take the full timeout. Size `max_wait` below the
client's own timeout.

## Shadow

`shadow_failover` uses the same keys and engine, independently of the Primary
setting. Shadow retries never delay the Primary response:

- HTTP Shadow requests run after the Primary response, bounded by
  `shadow_timeout`.
- gRPC Shadow streams are bounded by `grpc_shadow_timeout`. While a Shadow
  stream is still being created, request messages wait in the bounded Shadow
  queue; if it fills, the Shadow result becomes `queue_full`.
- The gRPC Shadow stream and its timeout start before the Primary stream is
  opened. When Primary failover takes longer than `grpc_shadow_timeout` (for
  example a blackholed target and a long `attempt_timeout`), the Shadow result
  becomes `timeout` for that call. Primary is not affected.
- The Shadow rate limiter is charged once per mirrored request, not per attempt.

Because the default Shadow timeouts are short, failover without backoff
(`retries: 0`) is usually the useful Shadow setting.

## Milter

Milter mode has a single Primary and a single Shadow address and stateful
per-connection sessions. Retry and failover are not supported there, and
`enabled: true` in either block is rejected at startup.

## Observability

- Result logs (`auth_proxy`, `grpc_proxy`) add `primary_attempts` and
  `shadow_attempts` when more than one attempt was made, and
  `primary_failover_from` and `shadow_failover_from` when the final target
  differs from the first attempted one. `primary_selected` and
  `shadow_selected` always name the target that produced the result. Without
  extra attempts the log records are unchanged.
- `primary_request_failed` (HTTP) includes `primary_attempts` when more than one
  attempt was made.
- Every connect attempt that failed in a repeatable way is logged as
  `backend_attempt_failed` with `protocol`, `backend`, `req_id`, `attempt`,
  `selected`, and `err`: at `WARN` for Primary and `DEBUG` for Shadow. This
  includes the last attempt of an exhausted request, which is then also
  reported by the normal failure log. A `least_conn` pool writes this record
  for every failure that starts a cooldown, also while failover is disabled.
- `doppelgaenger_backend_retries_total{protocol,target,result}` counts each
  extra attempt as `retry` (same target) or `failover` (different target), and
  each exchange whose attempts were all refused as `exhausted`. A request whose
  client went away during the attempts is not counted as `exhausted`.
- Backend spans keep one span per exchange, add a `backend.attempt.failed`
  event per failed attempt and the `doppelgaenger.backend.attempts` attribute,
  and report `server.address` of the final target.

`doppelgaenger_backend_requests_total` still counts one backend exchange per
request, with the status and result of the final attempt.

## Rolling upgrade recipe

```yaml
primary_base_urls:
  - "https://backend-a.example.test"
  - "https://backend-b.example.test"
  - "https://backend-c.example.test"
primary_selection_mode: "round_robin"
primary_failover:
  enabled: true
  retries: 2
  backoff_initial: "100ms"
  backoff_max: "1s"
  max_wait: "3s"
  cooldown: "10s"
```

1. Confirm that every backend shuts down gracefully and that the remaining
   backends can carry the full load.
2. Take one backend out: stop it gracefully, upgrade it, start it again.
3. Watch `doppelgaenger_backend_retries_total` and `backend_attempt_failed`
   logs. `failover` events are expected while a backend is down; `exhausted`
   events mean requests failed because no backend was reachable.
4. Wait until the upgraded backend serves traffic again (no new
   `backend_attempt_failed` logs for it after the cooldown), then continue with
   the next one.
