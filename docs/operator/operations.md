# Operations

This runbook keeps rollout and incident checks focused on the Primary safety
boundary. A green process is necessary, but it is not proof that backend
traffic and comparisons behave as intended.

## Preflight

Before changing a deployment:

1. Record the current binary or image version and configuration revision.
2. Confirm the active `protocol` and listener address.
3. Verify Primary and Shadow endpoints from the Doppelgaenger network context.
4. Start with `shadow_sample_percent: 0` for a new path.
5. Enable the Prometheus listener if readiness is required.
6. Review rules for the unmatched-path or unmatched-method Primary-only
   behavior.
7. Confirm that Shadow side effects are isolated or otherwise safe.
8. Keep the previous binary/image and configuration available for rollback.

For Milter, also verify the MTA's own failure policy and the backend's reply-turn
compatibility. For gRPC OIDC, verify the secret environment variable exists
without printing its value.

## Rollout sequence

1. Deploy with Shadow disabled.
2. Confirm listener readiness and absence of startup errors.
3. Run a protocol-level Primary smoke.
4. Enable one narrow rule or a small sample.
5. Generate a known request or transaction and confirm both selected backends.
6. Inspect comparison logs and counters for `same`, `diff`, `error`, and
   `skipped` as appropriate.
7. Watch Primary errors and latency before increasing Shadow volume.

Use the [HTTP](../tutorials/http-first-shadow.md),
[gRPC](../tutorials/grpc-first-shadow.md), or
[Milter](../tutorials/milter-rollout.md) tutorial for concrete probes.

## Readiness check

With Prometheus enabled on plaintext localhost:

```sh
curl --fail --silent --show-error \
  http://127.0.0.1:9464/healthz
```

Expected ready output resembles:

```json
{"status":"ok","protocol":"http","ready":true,"shutting_down":false}
```

This proves only that the selected proxy listener reached serving state.

## Protocol-level verification

HTTP verification should check the client-visible Primary status, selected
headers, body, and `X-Request-ID`, then confirm a corresponding Shadow result
log when Shadow was expected.

gRPC verification should check Primary response messages, headers/trailers, and
status. A Shadow comparison log must show whether Shadow completed; a Primary
OK alone does not prove that it did.

Milter verification must exercise a complete transaction through EOM and then
check both MTA behavior and Doppelgaenger's `milter_proxy` events. A bare TCP
connect is not a functional smoke.

## Interpreting Shadow states

- `shadow_enabled=false`: policy, sampling, or rate limiting selected
  Primary-only work.
- `shadow_enabled=true` and `shadow_started=false`: the decision allowed Shadow,
  but a session/target/queue condition prevented it from starting.
- `shadow_started=true` plus an error: Shadow began but did not complete.
- comparison `skipped`: no comparison was expected or possible.
- comparison `error`: Shadow or the comparator was incomplete or failed.
- comparison `diff`: both sides completed far enough to find a configured
  difference.

These states are not interchangeable. Alerting only on `diff` misses broken
Shadow coverage.

## Common failure paths

### Process does not start

Read the first configuration or listener error. Typical causes are a missing
file, incomplete TLS pair, invalid rule regex, missing required backend, port
collision, or inaccessible certificate inside a chroot/container.

### Readiness is missing

Confirm `observability.prometheus_enabled: true`, the observability bind address
and port, and that `prometheus_path` is not `/healthz`. Readiness is not served
on the protocol listener.

### HTTP returns 502 or 503

503 means the Primary session could not be created. 502 means the Primary
request/response failed after session creation. Check DNS, routing, TLS trust,
protocol negotiation, and backend availability from the proxy environment.

### gRPC is healthy but Shadow comparison errors

Inspect `shadow_skip_reason`, `shadow_err`, queue pressure, Shadow target TLS,
and `grpc_shadow_timeout`. Do not increase the timeout until the cause and
Primary latency isolation are understood.

### Milter client hangs or loses protocol synchronization

Stop the rollout and compare the backend's reply behavior with the documented
fixed no-reply profile. A Milter that replies to a command Doppelgaenger treats
as no-reply is not compatible with the current implementation.

### Reload appears successful but behavior is unchanged

For SIGHUP, search for `reload requested`, `reload aborted: invalid config`, and
`reload failed`. For the bundled systemd unit, remember that reload schedules a
restart asynchronously; verify the replacement service and readiness.

## Rollback

Restore the previous configuration and binary/image together, then repeat
readiness and the protocol smoke. If the incident is isolated to Shadow, the
narrowest containment is usually `shadow_sample_percent: 0` plus removal of
force access or a rule with `shadow: never`. This keeps the Primary proxy path
in place while stopping new Shadow work.
