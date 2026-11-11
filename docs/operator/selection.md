# Backend selection

A Primary or Shadow pool with more than one target uses a selection mode to
choose the target for each HTTP request or gRPC call. Both pools are configured
independently.

| Key | Protocol | Default |
| --- | --- | --- |
| `primary_selection_mode` | HTTP | `round_robin` |
| `shadow_selection_mode` | HTTP | `round_robin` |
| `primary_grpc_selection_mode` | gRPC | `round_robin` |
| `shadow_grpc_selection_mode` | gRPC | `round_robin` |

| Mode | Chooses | Use it when |
| --- | --- | --- |
| `round_robin` | The targets in turn. | Requests cost about the same and backends are equally fast. |
| `source_ip_hash` | The same target for the same client IP. | A client must stay on one backend. |
| `least_conn` | The target with the fewest requests in flight. | Request durations vary, backends differ in speed, or gRPC streams are long-lived. |

In HTTP mode an unknown value currently becomes `round_robin`. In gRPC mode an
unknown value is a startup error. Milter mode has one Primary and one Shadow
address and does not select.

Selection decides the first attempt. What happens when that attempt cannot
connect is described in [Retry and failover](failover.md).

## round_robin

Every target receives the same number of requests, whatever they cost. With
failover enabled, the share of an unreachable target goes to the next target in
the list, which then carries twice its normal share. Plan capacity for that, or
use `least_conn`.

## source_ip_hash

The client port is removed before hashing. Without a usable client IP, HTTP
falls back to the selector's normal behavior and gRPC to round robin. With
failover enabled, clients of an unreachable target are spread over the
remaining targets and return once it is reachable again.

## least_conn

```yaml
primary_selection_mode: "least_conn"      # HTTP
primary_grpc_selection_mode: "least_conn" # gRPC
```

Each request goes to the target that has the fewest requests in flight at that
moment. A slow or busy backend holds its requests longer and therefore
receives fewer new ones.

- **HTTP** counts a request from the start of the attempt until the response
  has been read. Requests are counted, not TCP connections, so keep-alive and
  HTTP/2 multiplexing do not distort the picture.
- **gRPC** counts a stream from its creation until it ends. A long-lived stream
  counts for as long as it is open.
- Targets with the same count are chosen at random. An idle pool therefore
  spreads its requests evenly over all targets, like `round_robin` does on
  average.
- The counts are kept per pool inside one proxy process. Several proxy
  instances do not share them, and traffic that reaches a backend from
  elsewhere is not seen.

There is nothing else to configure.

### Unreachable targets

An unreachable target has nothing in flight and would look like the best
choice. `least_conn` therefore always uses the cooldown of the pool's failover
block (`primary_failover.cooldown` or `shadow_failover.cooldown`, default
`10s`):

- After a connect failure the target is not selected for the cooldown, unless
  no other target is left.
- In HTTP mode the same happens when a backend accepts a new connection and
  then drops the request without a response, for example with a reset or
  closed connection. Such a request is never repeated. Two failures do not
  count. A timeout: a slow backend keeps its requests in flight and is avoided
  by its load alone. And a failure on a reused keep-alive connection: a healthy
  backend closes idle connections, and a request written in that moment says
  nothing about the backend.
- In gRPC mode a target whose channel reports transient failure is treated
  like a target in cooldown.
- The target stays out for the whole cooldown. Requests that were still
  running on it when it failed do not bring it back early: a backend that is
  shutting down finishes old requests while it refuses new ones.
- All of this also applies while `enabled` is `false`. The request that found
  the target unreachable still fails, because without failover there is only
  one attempt. Later requests avoid the target.
- Each failure that starts a cooldown is logged as `backend_attempt_failed`,
  also while failover is disabled. It tells you which target left the
  selection and why.
- `cooldown: 0` together with `least_conn` is a startup error.

With failover enabled, a failed attempt moves to the least busy remaining
target. The share of an unreachable target is spread over all remaining
targets instead of going to its neighbour in the list.

### Things to know

- A backend that answers quickly receives more requests. This includes a
  backend that answers every request with an error status right away: a status
  is a response, not a connect failure, and never starts a cooldown. If a
  backend can fail that way, `round_robin` limits it to its normal share.
- A target that becomes reachable again starts with nothing in flight. It
  receives the new requests until it has caught up with the other targets. The
  burst is as large as the number of requests in flight on one of them.
- With `max_targets: 1` a request is retried on the target that was selected
  for it and never moves, as with the other modes.
- `least_conn` does slightly more work per request than `round_robin`, also
  while failover is disabled, because every attempt is tracked. The difference
  is far below the cost of the backend round trip.
- A release that does not know `least_conn` treats the value like any unknown
  one: HTTP mode silently uses `round_robin`, gRPC mode refuses to start.
  Change the value back before you roll back to such a release.
