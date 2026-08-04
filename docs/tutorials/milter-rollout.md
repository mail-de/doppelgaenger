# Tutorial: roll out Doppelgaenger in front of a Milter

This tutorial is a rollout sequence rather than a copy-and-paste MTA mutation.
Mail topology, MTA failure policy, and backend addresses differ between
deployments. Preserve those local decisions and change only the Milter hop.

## 1. Record the current path

Before editing anything, record:

- the MTA setting that points to the current Primary Milter;
- the MTA's Milter failure action and timeouts;
- the current Primary Milter version and listener address;
- one controlled message that proves the present path through final delivery.

Do not continue if the MTA behavior after a broken Milter connection is unknown.

## 2. Start Primary-only

Create a dedicated configuration using real addresses:

```yaml
protocol: milter
milter_listen_addr: "127.0.0.1:19999"
primary_milter_addr: "127.0.0.1:11332"
shadow_milter_addr: "127.0.0.1:21332"
milter_timeout: "15s"
shadow_sample_percent: 0
shadow_rps: 0
log_json: true
observability:
  prometheus_enabled: true
  prometheus_address: "127.0.0.1"
  prometheus_port: 19464
  prometheus_path: "/metrics"
```

Start Doppelgaenger and check `http://127.0.0.1:19464/healthz`. Then point only
the intended MTA Milter entry at `127.0.0.1:19999`, keeping the original Milter
endpoint as Doppelgaenger's Primary.

## 3. Prove the Primary transaction

Send a controlled message through the real SMTP entry point. Verify all of the
following:

- the SMTP outcome is the expected one;
- the message reaches its expected final state;
- the Primary Milter processed it;
- Doppelgaenger logged the option-negotiation and EOM reply turns;
- no Shadow session was selected.

This step is the rollback gate. If it fails, restore the MTA's direct Primary
Milter setting before investigating.

## 4. Enable a bounded Shadow sample

Set a small percentage, for example:

```yaml
shadow_sample_percent: 1
shadow_rps: 1
shadow_burst: 1
```

Reload or restart using the documented deployment path. Send several controlled
messages until one connection shows `shadow_enabled=true`. Confirm
`shadow_started=true`, `shadow_ok=true`, and inspect `diff` plus
`decision_diff`.

Milter sampling is per accepted MTA connection, not per message or frame. A
pooled MTA connection therefore keeps its initial Shadow decision.

## 5. Watch the right failure signals

Treat these as loss of Shadow coverage even when mail continues through Primary:

- `milter_shadow_session_failed`
- `milter_shadow_queue_full`
- `shadow_started=false` after Shadow was enabled
- non-empty `shadow_err` or `compare_err`

Treat `milter_primary_failed`, `milter_primary_empty`, and client connection
closures as Primary-path incidents. The MTA's configured Milter failure policy
then controls the SMTP result.

Only raise the sample after complete SMTP-to-final-state proof remains healthy.
For the exact protocol boundary, read [Milter mode](../operator/milter.md).
