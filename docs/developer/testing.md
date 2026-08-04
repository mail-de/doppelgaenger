# Testing

Tests are layered so protocol logic can fail quickly while real process and
transport behavior remains covered.

## Local quality targets

| Target | Purpose |
| --- | --- |
| `make test` | Verbose Go unit and package tests with vendored modules |
| `make race` | Short test suite under the race detector |
| `make vet` | Go vet with vendored modules |
| `make lint` | Full `golangci-lint run ./...` |
| `make build-check` | Build every package |
| `make guardrails` | Format, vet, lint, test, race, and build check |

`make fix` runs `gofmt` across non-vendored Go files and therefore changes the
working tree when formatting is needed.

## End-to-end targets

| Target | Verified behavior |
| --- | --- |
| `make e2e-http` | Real proxy/fake-backend processes, Primary and forced Shadow routing, rules, path mapping, body limits, headers, trace propagation, Prometheus, and OTLP |
| `make e2e-grpc` | Unary and streaming calls, rules, metadata overlays, Shadow errors/timeouts, Primary preservation, comparison modes, trace propagation, Prometheus, and OTLP |
| `make e2e-milter` | Primary/Shadow frames, sampling, comparison, metrics, OTLP, and a complete transaction against the fixed no-reply profile |
| `make e2e-docker` | Dockerfile, Compose port, mounted configuration, and listener agreement |
| `make e2e` | All four targets in sequence |

The no-reply-profile E2E uses a repository-owned backend. It has no external
image dependency and always exercises option negotiation, state commands, and
a multi-frame EOM response.

## Choosing test scope

Configuration changes need focused cases for accepted, normalized, and rejected
values. Rule changes need first-match, unmatched, force, and explicit-disable
coverage. Shadow concurrency changes need a test that proves client-visible
Primary progress follows the protocol's documented timing boundary and that
overload is bounded.

Milter changes must distinguish no-reply commands from reply turns and cover
multi-frame EOM replies. gRPC changes must cover unary plus each affected stream
direction. HTTP response changes should cover redirects, compressed response
headers, hop-by-hop removal, and body limits where relevant.

## Test tools

E2E packages build their helper binaries directly. The Makefile exposes only
the main binary and fake HTTP backend as ordinary build targets. See
[Test tools](test-tools.md) for all helpers and flags.
