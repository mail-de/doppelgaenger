# Test tools

The commands under `cmd/` are black-box helpers for development and E2E tests.
They are not part of the production proxy API.

## Build helpers

The Makefile builds the main binary and fake HTTP server:

```sh
make build build-fake
```

Build the remaining helpers directly because no ordinary Makefile target exists
for them:

```sh
go build -mod=vendor -o build/fakegrpcserver ./cmd/fakegrpcserver
go build -mod=vendor -o build/grpcprobe ./cmd/grpcprobe
go build -mod=vendor -o build/fakemilterserver ./cmd/fakemilterserver
go build -mod=vendor -o build/milterprobe ./cmd/milterprobe
go build -mod=vendor -o build/fakeotlpcollector ./cmd/fakeotlpcollector
```

The E2E suites build the helpers they need in temporary directories.

## `fakehttpserver`

This HTTP backend always returns status 200 with body `ok`. It can echo selected
request headers, add fixed response headers, and randomly change selected header
values.

It loads configuration from `CONFIG_FILE`, then `./fakehttpserver.yaml`, then
`/etc/doppelgaenger/fakehttpserver.yaml`.

| Key | Default | Meaning |
| --- | --- | --- |
| `listen_addr` | `:9001` | HTTP listener. |
| `tls_cert_file` | empty | Optional TLS certificate. TLS is used only when both files are set. |
| `tls_key_file` | empty | Optional TLS private key. |
| `mode` | `echo` | `echo` or `random`. |
| `echo_headers` | auth/session header list | Request headers copied to the response. Empty restores the default list. |
| `response_headers` | `{}` | Fixed response headers. Empty header names are rejected. |
| `random_chance` | `10` | Per-header percentage in random mode; clamped to 0 through 100. |
| `random_headers` | `echo_headers` | Headers eligible for mutation. Empty uses the echo list. |
| `random_values` | `random` | Value pool. Empty restores `random`. |
| `log_json` | `true` | JSON or text `slog` output. |

Example files are `fakehttpserver.yaml`, `fakehttpserver-primary.yaml`, and
`fakehttpserver-shadow.yaml`.

## `fakegrpcserver`

This is a descriptor-free gRPC backend with JSONL request summaries on stdout.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `127.0.0.1:9445` | TCP listener. |
| `-mode` | `auto` | `auto`, `unary-echo`, `server-stream-count`, `client-stream-count`, or `bidi-echo`. |
| `-status-code` | `OK` | Final status name or number. |
| `-status-message` | empty | Final status message. |
| `-status-metadata-key` | `x-fake-status` | Incoming metadata that overrides status. |
| `-delay-metadata-key` | `x-fake-delay` | Incoming Go duration that delays the response. |
| `-response-prefix` | empty | Prefix for response payloads. |
| `-stream-count` | `3` | Server-stream response count; must not be negative. |
| `-header` | none | Repeatable response header `key=value`. |
| `-trailer` | none | Repeatable response trailer `key=value`. |
| `-log-metadata-key` | none | Repeatable incoming metadata key included in logs. |
| `-tls-cert` | empty | Server certificate. |
| `-tls-key` | empty | Server key. |
| `-client-ca` | empty | Client CA for mTLS. |
| `-require-client-cert` | `false` | Require and verify client certificates. |

In `auto` mode, method names containing `ServerStream`, `ClientStream`, or
`Bidi` select the matching behavior; all other methods use unary echo. TLS
requires both certificate and key. mTLS settings require TLS.

## `grpcprobe`

This client sends raw message bytes to any full gRPC method and checks the
response without generated stubs.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `127.0.0.1:9444` | gRPC target. |
| `-method` | empty | Required full method beginning with `/`. |
| `-mode` | `unary` | `unary`, `server-stream`, `client-stream`, or `bidi`. |
| `-payload` | empty payload | Repeatable request payload. |
| `-metadata` | none | Repeatable `key=value` request metadata. |
| `-expect-status` | `OK` | Expected status name or number. |
| `-expect-count` | not asserted unless supplied | Expected response-message count. |
| `-expect-message` | none | Repeatable exact response payload. |
| `-print-metadata` | `false` | Include response headers and trailers in JSON output. |
| `-timeout` | `5s` | Positive RPC timeout. |
| `-tls` | `false` | Enable TLS 1.2 or later. |
| `-root-ca` | empty | Optional root CA PEM. |
| `-server-name` | empty | TLS server-name override. |
| `-insecure-skip-verify` | `false` | Disable certificate verification. |

The probe does not currently support a client certificate for mTLS.

## `fakemilterserver`

This lightweight backend reads frames, records optional JSONL entries, and
returns one configured decision frame for every input frame.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `127.0.0.1:19997` | TCP listener. |
| `-decision` | `accept` | `accept`, `reject`, `tempfail`, `discard`, or `continue`. |
| `-log-file` | empty | Optional JSONL frame log. |

Because it replies to every frame, this helper is not a model of the fixed
no-reply profile. The Milter E2E suite has a repository-owned backend for that
contract.

## `milterprobe`

This helper writes one framed command and expects one decision reply.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `127.0.0.1:19999` | Doppelgaenger Milter address. |
| `-command` | `c` | First byte used as the command. |
| `-payload` | `e2e` | Raw string payload. |
| `-expect-decision` | `accept` | Expected normalized decision. |
| `-timeout` | `2s` | Network deadline. |

It is a focused fake-backend probe, not a complete Milter transaction tool. Do
not use it to claim compatibility with an MTA or a production Milter backend.

## `fakeotlpcollector`

This OTLP/HTTP test receiver records trace and metric names in memory.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-listen` | `127.0.0.1:14318` | HTTP listener. |
| `-log-file` | empty | Optional JSONL collection log. |

Endpoints are:

- `GET /healthz`: returns `ok`;
- `GET /summary`: returns collected request counts, span names, trace IDs, and
  metric names;
- `POST /v1/traces`: accepts protobuf OTLP trace exports;
- `POST /v1/metrics`: accepts protobuf OTLP metric exports.

It is intentionally minimal and is not a production collector.
