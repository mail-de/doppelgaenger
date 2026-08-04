# Frequently asked questions

## Can Shadow ever change the client response?

A successful Primary response remains authoritative. Shadow errors, timeouts,
or differences do not replace it. Primary failures are still client-visible:
HTTP returns 502 or 503, gRPC returns the Primary-side error/status, and Milter
closes the affected client connection instead of inventing a successful reply.

## Is it safe to Shadow state-changing traffic?

Not automatically. Shadow receives a real copy of eligible traffic and may
perform real writes, send messages, consume quotas, or contact third parties.
Use isolated Shadow data or application-level safeguards before mirroring such
traffic.

## Does sampling store skipped traffic for later replay?

No. Sampling and rate limiting are immediate decisions. Skipped work is not
persisted.

## Does one process serve HTTP, gRPC, and Milter at the same time?

No. `protocol` selects exactly one proxy listener. The optional observability
HTTP listener is separate.

## Why is an unmatched HTTP path or gRPC method not shadowed?

Once a non-empty rule list exists, unmatched traffic defaults to Primary-only.
Add an explicit catch-all rule if the remaining traffic should use another
policy.

## Does forced Shadow traffic respect the rate limit?

Forced HTTP and gRPC traffic bypasses the Shadow token bucket. A matching rule
with `shadow: never` still blocks it. Restrict who can send the force header or
metadata key.

`shadow: always` is not the same as forced traffic: it skips sampling but still
uses the limiter.

## Why is `/healthz` unavailable?

It exists only on the optional Prometheus listener. Enable
`observability.prometheus_enabled` and keep `prometheus_path` different from
`/healthz`. It is not served on the protocol listener.

## Does readiness prove that the backends are healthy?

No. It proves that the selected local proxy listener reached ready state. Run a
protocol-level smoke to prove Primary and inspect Shadow logs or metrics to
prove comparison coverage.

## Why does HTTP show different status codes but `diff=false`?

The current HTTP comparators do not include status equality in `diff`. They
compare selected headers and, for JSON or HTML modes, bodies. Both status codes
are still present in `auth_proxy` logs.

## Does non-strict JSON comparison ignore array order?

No. It ignores JSON object key order by comparing decoded objects, but array
positions remain significant. Strict mode compares compact JSON bytes.

## Can gRPC comparison show which protobuf field changed?

No. Generic gRPC mode has no descriptors and treats messages as bytes. It can
compare final status, selected metadata, response-message count, or a hash of
the response-message sequence.

## Why does gRPC configuration require a Shadow target when sampling is zero?

It does so only when another effective path can enable Shadow, such as a force
metadata key or a rule with `shadow: always`. Remove those paths or configure a
Shadow target.

## Is the Milter implementation generic?

Its framing is generic, but its reply-turn behavior uses the fixed no-reply
profile documented in the Milter operator guide. Compatibility with a backend
that replies to different transaction commands must be proven with a complete
transaction.

## Is Milter sampling per message?

No. It is decided once per accepted client connection. All frames on that
connection share the decision.

## What happens after a Milter Shadow failure?

Shadowing stops for the remainder of that client connection so later stateful
frames cannot be sent out of order. Primary processing continues.

## Does `log_only_on_diff` suppress every log?

No. It applies to HTTP and gRPC result logs. Lifecycle and error logs remain,
and Milter currently logs every frame result.

## Can configuration be validated without opening a listener?

There is no dedicated validation subcommand. Use the exact binary and candidate
file in a controlled preflight, then verify listener readiness. YAML parsing
alone is insufficient, and the current loader does not reject unknown keys.

## Is SIGHUP a connection-preserving reload?

No. On Unix it validates and then re-executes the process. The PID is preserved,
but active connections and protocol sessions are not. The bundled systemd unit
maps reload to an asynchronous restart instead.

## Where should secrets be stored?

Use the deployment platform's secret mechanism. Prefer the gRPC OIDC
`client_secret_env` field. Do not commit private keys, inline client secrets,
Prometheus credentials, or OTLP authorization headers.

## Which file is the full configuration example?

The repository root [`config.yaml`](../config.yaml) is the annotated example.
`config.docker.yaml` is specifically the local Docker Compose HTTP setup.

## What license applies?

Doppelgaenger's project-owned source code and documentation are available under
the [MIT License](../LICENSE), copyright 2026 mail.de GmbH. Keep the
copyright and license notice with copies or substantial portions of the
software.

Vendored dependencies remain subject to their own license terms. The project
license does not replace or relicense notices shipped under `vendor/`.
