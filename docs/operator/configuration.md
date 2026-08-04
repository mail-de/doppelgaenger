# Configuration

This guide covers configuration loading and settings shared across protocols.
Protocol-specific fields are documented in the [HTTP](http.md),
[gRPC](grpc.md), and [Milter](milter.md) guides. The annotated
[`config.yaml`](../../config.yaml) contains every top-level application setting
and is the safest starting point for a new file.

## Loading a file

Doppelgaenger reads exactly one YAML file. Lookup order is:

1. `--config <path>` or `-c <path>`
2. The path in `CONFIG_FILE`
3. `config.yaml` in the current working directory
4. `/etc/doppelgaenger/config.yaml`

The CLI path is implemented by setting `CONFIG_FILE` before application startup,
so both mechanisms use the same loader. A missing, unreadable, malformed, or
invalid file prevents startup.

The current loader does not reject unknown YAML keys. A misspelled key can
therefore be ignored rather than reported. Keep local files close to the
annotated example and review configuration diffs carefully.

## Protocol selection

| Key | Default | Meaning |
| --- | --- | --- |
| `protocol` | `http` | Active listener and proxy implementation: `http`, `grpc`, or `milter` |

Only one protocol listener is active in a process. Settings for the other modes
may remain in the same file, but they do not start additional proxy listeners.
The separate observability listener can run in every mode.

## Shared Shadow controls

| Key | Default | Meaning |
| --- | --- | --- |
| `shadow_sample_percent` | `5` | Sampling percentage. Values below 0 become 0; values above 100 become 100. |
| `shadow_rps` | `200` | Token-bucket rate. `0` or a negative value disables the limiter. |
| `shadow_burst` | `400` | Token-bucket capacity. When limiting is enabled, values below 1 become 1. |

The limiter is process-local. HTTP consumes a token for a sampled request,
gRPC for a sampled RPC, and Milter for a sampled client connection. Forced HTTP
and gRPC Shadow work bypasses the limiter. `shadow: always` does not bypass it.

## Logging

| Key | Default | Applies to | Meaning |
| --- | --- | --- | --- |
| `log_json` | `true` | All modes | Emit JSON through Go `slog`; `false` selects text output. |
| `log_only_on_diff` | `false` | HTTP and gRPC result logs | Suppress clean result logs. Differences, Shadow errors, and comparison errors remain visible. |
| `log_session_only_on_diff` | `true` | HTTP result logs | Hide `X-Session-ID` values for clean, non-forced comparisons. |

Lifecycle and error messages are not governed by `log_only_on_diff`. Milter
result logging is also not suppressed by that setting.

## Runtime isolation

| Key | Default | Meaning |
| --- | --- | --- |
| `run_as_user` | empty | On Unix, switch to a user name or numeric UID after initialization. |
| `run_as_group` | empty | On Unix, switch the primary GID when `run_as_user` is not set. |
| `chroot` | empty | On Unix, change the process root before changing identity. |

These controls require the privileges needed for `chroot`, `setgroups`,
`setgid`, or `setuid`. The implementation order is chroot, supplementary
groups, GID, then UID.

There is one important current constraint: when `run_as_user` is set, the
implementation changes the UID and supplementary groups, but it does not change
the process's primary GID and does not apply `run_as_group`. Do not combine the
two fields expecting a group override. With only `run_as_group`, the GID changes
while the UID remains unchanged.

A configured chroot must contain `/etc/hosts`, `/etc/resolv.conf`, and
`/etc/nsswitch.conf`; startup fails after entering the jail if any is absent.
All configured paths needed after the chroot, including certificates and CAs,
must resolve inside it.

Runtime isolation is marked as applied in the environment so a SIGHUP re-exec
does not attempt the privileged operations a second time.

## Duration and address syntax

Durations use Go duration strings such as `150ms`, `2s`, or `1m`. Listener and
backend TCP addresses use `host:port`; an empty host such as `:8080` binds all
available interfaces. HTTP backend entries are URLs and must include a scheme
and host.

## Validation behavior

Some invalid numeric settings are normalized rather than rejected. Examples
include the Shadow percentage and HTML threshold. Protocol enums, malformed
rules, invalid header names, incomplete TLS pairs, and missing required
backends are rejected where the configuration code validates them.

There is no standalone `config validate` command. The supported preflight is to
start the exact binary with the candidate file in a controlled environment and
confirm that it reaches its listener, or to exercise the configuration through
the repository tests. Never infer production validity from YAML parsing alone.

## Reload semantics

On Unix, SIGHUP loads and validates the same configured file. If validation
succeeds, Doppelgaenger replaces its process image with `exec`; the PID is
preserved and all components are rebuilt. If validation fails, the running
process stays in place and logs `reload aborted: invalid config`.

This is not an in-memory partial reload. Connections and protocol sessions are
not preserved by the re-exec. The bundled systemd unit deliberately maps
`systemctl reload` to an asynchronous service restart instead; see
[Deployment](deployment.md).
