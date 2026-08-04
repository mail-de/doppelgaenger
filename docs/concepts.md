# How Doppelgaenger works

## Purpose

Doppelgaenger lets an operator exercise a candidate backend with a controlled
copy of real traffic while keeping the established backend authoritative. It is
a proxy, sampler, and comparison point. It is not a traffic switch, a replay
database, or an automatic promotion system.

The documentation uses two fixed terms:

- **Primary**: the backend whose result is authoritative and client-visible.
- **Shadow**: the candidate backend whose result is observed but never returned
  in place of a successful Primary result.

## Request flow

For each HTTP request, gRPC RPC, or Milter client connection, Doppelgaenger
first applies the active protocol policy. It always sends eligible work to the
Primary backend. Shadow sampling, force controls, rules, and rate limiting then
decide whether Shadow work starts.

HTTP Shadow requests run asynchronously after the Primary response has been
prepared. gRPC Primary messages are forwarded while Shadow runs, but final RPC
completion can wait up to `grpc_shadow_timeout` so the handler can collect the
comparison result. Milter Shadow frames are queued and processed in order for
that Milter connection because a Milter session is stateful.

When both results are available, a protocol-specific comparator records one of
`same`, `diff`, `error`, or `skipped`. The result is available through
structured logs and, when enabled, metrics and traces.

## Guarantees

Doppelgaenger is designed around these observable guarantees:

- A successful Primary result remains the client-visible result.
- Shadow timeouts and failures do not replace a successful Primary result.
- HTTP and gRPC rules use first-match-wins evaluation.
- A configured rule list changes the unmatched default to Primary-only.
- The gRPC and Milter Shadow queues are bounded; overload is reported instead
  of allowing unbounded queue growth.
- Milter Shadow frames remain ordered within each client connection.
- Configuration is validated before listeners start and before a SIGHUP
  re-exec reload.

## Limits and external responsibilities

The guarantees stop at the process boundary:

- Doppelgaenger cannot make a state-changing Shadow backend safe. Operators
  must use isolated data, idempotent endpoints, or other application controls.
- Sampling is not durable replay. Requests skipped by sampling or rate limits
  are not stored for later delivery.
- `/healthz` reports local listener readiness. It does not query Primary,
  Shadow, an identity provider, or an OpenTelemetry collector.
- Milter handling does not decide an MTA's final behavior after a broken Primary
  connection. The MTA's own Milter failure policy remains authoritative.
- Generic gRPC mode treats protobuf messages as opaque bytes. It does not know
  field names and cannot produce field-level protobuf diffs.
- HTTP comparison covers only the configured headers and the selected body
  comparison mode. It is not a semantic application test.

## Choosing a Shadow policy

Start with `shadow_sample_percent: 0`, enable observability, and verify the
Primary path. Then allow a narrowly scoped rule or a small sample. A force
header or force metadata key is useful for deliberate probes, but should be
accepted only from trusted callers because forced HTTP and gRPC traffic bypasses
the Shadow rate limiter. A rule with `shadow: never` still blocks forcing.

Milter has no force carrier. Its sample and rate-limit decision is made once per
accepted client connection.
