# Doppelgaenger

Doppelgaenger is a shadow proxy for comparing a production backend with a
candidate backend. Clients receive only the Primary result. Shadow work is
best-effort and is used for logs, metrics, traces, and comparison.

It supports three mutually exclusive runtime modes:

| Mode | Primary path | Shadow comparison |
| --- | --- | --- |
| HTTP | Status, selected headers, and body are returned to the client | Headers, JSON, or rendered HTML text |
| gRPC | Headers, messages, trailers, and status are returned to the client | Status, selected metadata, message count, or message hash |
| Milter | Complete Primary reply sequences are returned to the MTA | Reply decision and raw reply frames |

The central safety rule is simple: Shadow must not change the Primary result.
A Shadow timeout, connection error, or full Shadow queue is observable, but it
does not replace a successful Primary response.

## Start here

1. Read [How Doppelgaenger works](docs/concepts.md), especially the guarantees
   and limits.
2. Choose the protocol guide: [HTTP](docs/operator/http.md),
   [gRPC](docs/operator/grpc.md), or [Milter](docs/operator/milter.md).
3. Copy and edit the annotated [configuration example](config.yaml).
4. Follow the [deployment](docs/operator/deployment.md) and
   [operations](docs/operator/operations.md) guides.

For a guided first run, use one of the small
[tutorials](docs/README.md#tutorials). Common questions are answered in the
[FAQ](docs/faq.md).

## Minimal local run

The repository uses vendored Go modules and requires Go 1.26.5 or later.

```sh
make build
./build/doppelgaenger --config ./config.yaml
```

Configuration lookup order is:

1. `--config` or `-c`
2. `CONFIG_FILE`
3. `./config.yaml`
4. `/etc/doppelgaenger/config.yaml`

The selected file must exist and pass validation. Doppelgaenger exits instead
of starting with an unreadable or invalid configuration.

## Documentation

The [documentation index](docs/README.md) separates material by task and
audience:

- Operators: configuration, protocol behavior, deployment, observability, and
  day-two operations
- Tutorials: small end-to-end examples for HTTP, gRPC, and Milter
- Developers: architecture, repository workflow, tests, and bundled test tools

The root README intentionally stays short. Detailed behavior belongs in the
linked documents so it can be reviewed without turning this page into a second
configuration file.

## Development checks

Use the Makefile targets; they are the repository contract.

```sh
make test
make race
make lint
make guardrails
```

`make guardrails` formats the code, runs vet, lint, unit tests, race tests, and
build checks. Protocol E2E targets are documented in
[Testing](docs/developer/testing.md).

## Maintainer

Christian Rößner <c.roessner@team.mail.de>

## License

Doppelgaenger is licensed under the [MIT License](LICENSE).
