# Architecture

## Process composition

`main.go` parses the small CLI and builds the application with Fx. Configuration,
logging, health state, observability, selectors, protocol adapters, handlers,
and servers are constructed once at startup. All three protocol servers are
wired, but only the server matching `config.protocol` starts a listener.

The main packages have these responsibilities:

| Package | Responsibility |
| --- | --- |
| `internal/config` | YAML defaults, decoding, normalization, and validation |
| `internal/app` | dependency providers, logger, rate limiter, reload, socket activation, and Unix runtime isolation |
| `internal/server` | HTTP listener lifecycle |
| `internal/proxy` | HTTP ingress, policy, Primary response, detached Shadow work, and result logging |
| `internal/backend` | HTTP target selection and transport |
| `internal/pathrules` | ordered HTTP rule resolution |
| `internal/mapping` | direct or regex HTTP path mapping |
| `internal/grpcproxy` | generic gRPC listener, targets, rules, streams, comparison, OIDC, and logging |
| `internal/milterproxy` | Milter listener, client connections, Primary path, and per-connection Shadow worker |
| `internal/protocol` | shared event/result model plus HTTP and Milter adapters/comparators |
| `internal/compare` | HTTP header, JSON, and HTML comparators |
| `internal/observability` | Prometheus registry, OTLP providers, spans, metrics, and trace carriers |
| `internal/health` | local readiness state and handler |

## HTTP data path

The HTTP handler reads and bounds the request body, resolves a path rule, maps
Primary and Shadow paths, and prepares forwarding headers. It creates a Primary
session and returns the completed Primary response. Only then does it run a
selected Shadow request in a goroutine with a context detached from client
cancellation and bounded by `shadow_timeout`.

HTTP target selection and upstream transport live under `internal/backend`.
The protocol adapter owns request construction. The comparator registry allows
a rule to choose header, JSON, or HTML comparison per request.

## gRPC data path

The gRPC server uses a raw codec and an unknown-service handler, so it does not
need generated stubs or descriptors. It selects a Primary target, creates the
upstream stream, and forwards client and server messages. A Shadow forwarder
receives request messages through a bounded queue and collects only the state
needed by the configured comparator.

Primary response headers, messages, trailers, and status remain client-visible.
The Shadow stream is observed separately. OIDC token injection is a Primary
metadata concern and is applied before the Primary upstream call.

## Milter data path

The Milter listener creates one Primary backend session per accepted client
connection. Incoming frames are passed to Primary synchronously. Commands in
the fixed no-reply set perform only a write; option negotiation, EOM, and
unknown reply-turn commands also read a complete response sequence.

If Shadow is selected for the connection, a fixed-capacity worker queue
serializes frames onto one lazily opened Shadow session. Queue overflow or a
Shadow failure disables the worker for the rest of that connection.

## Cross-cutting safety boundaries

- Rules and target selectors are deterministic after configuration is loaded.
- The token bucket is process-local and shared by the active protocol path.
- HTTP and gRPC detached Shadow contexts intentionally survive client
  cancellation but retain configured Shadow deadlines.
- Observability must never add fields to opaque Milter payloads.
- gRPC metadata overlays reject transport-reserved and binary keys.
- Result logs should contain bounded, selected comparison material rather than
  arbitrary request metadata or full gRPC messages.

## Lifecycle

The active protocol marks readiness after its listener and server state are
initialized. Listener failure marks it not ready and asks Fx to shut down.
Observability starts its own listener when enabled and flushes providers during
shutdown.

On Unix, SIGHUP validates configuration and re-executes the current binary.
Socket activation consumes file descriptors passed by systemd and selects them
by protocol name, expected port, or deterministic fallback.
