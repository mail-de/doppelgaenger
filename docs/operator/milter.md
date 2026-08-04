# Milter mode

Milter mode places Doppelgaenger between an MTA and a Primary Milter backend.
It can mirror the same connection state to a Shadow Milter and compare reply
sequences without placing Shadow latency in the Primary reply path.

## Compatibility boundary

The current Milter turn model uses a fixed no-reply profile. It treats option
negotiation (`O`) and end-of-message (`E`) as reply turns. The following MTA
commands are treated as no-reply state updates:

`A`, `B`, `C`, `D`, `H`, `K`, `L`, `M`, `N`, `Q`, `R`, `T`, and `U`.

This is implemented behavior, not dynamic interpretation of the flags returned
during option negotiation. Do not assume compatibility with a Milter that
expects a reply for one of those commands. Validate any other implementation
with a complete SMTP/Milter transaction before production use.

## Configuration

```yaml
protocol: milter
milter_listen_addr: "127.0.0.1:9999"
primary_milter_addr: "127.0.0.1:9997"
shadow_milter_addr: "127.0.0.1:9998"
milter_timeout: "2s"
shadow_sample_percent: 0
shadow_rps: 200
shadow_burst: 400
```

| Key | Default | Meaning |
| --- | --- | --- |
| `milter_listen_addr` | `:9999` | TCP listener used by the MTA. |
| `primary_milter_addr` | `127.0.0.1:9997` | Primary Milter TCP backend. |
| `shadow_milter_addr` | `127.0.0.1:9998` | Shadow Milter TCP backend. |
| `milter_timeout` | `2s` | Dial, read, write, and Shadow event timeout when positive. |
| `shadow_sample_percent` | `5` | Per-client-connection Shadow sampling percentage. |
| `shadow_rps` | `200` | Process-local rate limit applied when deciding whether to Shadow a new Milter connection. |
| `shadow_burst` | `400` | Rate-limit burst capacity. |

Milter mode currently uses plaintext TCP. HTTP TLS fields and gRPC TLS fields do
not add TLS to the Milter listener or backends.

There is no Milter force field and no per-command rule list. Sampling and the
rate limiter decide once when a client connection is accepted. If that decision
is Primary-only, no Shadow session is opened for the connection.

## Primary reply handling

Every client frame is written to the existing Primary backend session in order.
For option negotiation and EOM, Doppelgaenger reads a complete reply sequence.
EOM action frames are collected until a terminal disposition frame is reached,
then the entire raw sequence is written to the MTA. This preserves actions such
as header or body changes before the final decision.

For the no-reply commands listed above, Doppelgaenger writes the frame to
Primary but neither waits for nor emits a reply.

If the Primary session cannot be opened, an expected Primary reply is empty, or
a Primary read/write fails, Doppelgaenger closes the client connection. It does
not synthesize `accept`, `continue`, or another fail-open reply. The MTA's own
Milter failure policy determines the resulting SMTP behavior, so configure and
test that policy explicitly.

## Shadow handling

Shadow processing uses one worker and one ordered queue per client connection.
The queue capacity is currently fixed at 64 frames and is not a configuration
field. The Shadow connection is opened lazily when the first queued frame is
processed.

Primary reply work completes before the corresponding frame is submitted to
the Shadow queue. A slow Shadow therefore cannot delay a Primary reply. The
worker still preserves frame order for the stateful Shadow session.

If the queue fills, a Shadow session cannot be opened, or a Shadow operation
fails, Doppelgaenger stops Shadow processing for the remainder of that client
connection. It logs the condition and leaves the Primary session active.

## Comparison and logs

For reply turns, comparison checks both the normalized terminal decision and
the complete raw reply byte sequence. A different action frame therefore
produces a raw difference even if both terminal decisions match. No-reply turns
normally compare as empty Primary and Shadow responses after both writes
succeed.

The `milter_proxy` result log includes the command, selected backends, Shadow
state, Primary and Shadow decisions, raw-difference flag, decision-difference
flag, and comparison error. `milter_shadow_queue_full` and
`milter_shadow_session_failed` identify connection-local Shadow degradation.

`log_only_on_diff` does not currently suppress Milter result logs.

## Production verification

A TCP connect test proves only that a listener exists. A useful Milter smoke
must include option negotiation, the transaction state commands used by the
MTA, EOM, and inspection of the resulting mail or MTA log. The repository E2E
suite includes a repository-owned backend that exercises the complete fixed
no-reply profile, including option negotiation and a multi-frame EOM response.

See [the Milter rollout tutorial](../tutorials/milter-rollout.md) for a safe
sequence.
