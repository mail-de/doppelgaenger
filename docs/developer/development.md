# Development

## Repository contract

This is a vendored Go project. Keep `go.mod`, `go.sum`, `vendor/`, Docker
builds, Makefile targets, and operator documentation aligned.

Project-owned source code and documentation are licensed under the
[MIT License](../../LICENSE). Preserve its copyright and permission notice in
copies or substantial portions. Vendored dependencies retain their own license
terms.

Read `AGENTS.md` and `POLICY.md` before making changes. The important working
rules are:

- use Makefile targets where they exist;
- add a focused regression test before a practical bug fix;
- run Go tests through the Makefile;
- run lint through `make lint`;
- keep comments and technical documentation in English;
- preserve the vendored dependency tree.

## Toolchain

The current build surfaces use Go 1.26.3 and `GOENV=greenteagc`. Normal Makefile
builds use `-mod=vendor`.

```sh
make build
make test
make race
make lint
```

After a dependency change:

```sh
go mod tidy
go mod vendor
```

Review both module metadata and the complete vendor diff.

## Guardrails

```sh
make guardrails
```

The target runs, in order:

1. `make fix`
2. `make vet`
3. `make lint`
4. `make test`
5. `make race`
6. `make build-check`

All findings must be fixed. Do not hide a lint or race failure behind a narrower
ad hoc command.

## Makefile target reference

| Target | Action |
| --- | --- |
| `all` | Build `doppelgaenger` and `fakehttpserver`. |
| `build` | Build `build/doppelgaenger` with the Git-derived version. |
| `build-fake` | Build `build/fakehttpserver`. |
| `clean` | Remove those two built binaries. |
| `fix` | Run `gofmt` on every non-vendored Go file. |
| `vet` | Run `go vet` with vendored modules. |
| `lint` | Run the repository `golangci-lint` configuration. |
| `test` | Run all Go package tests verbosely. |
| `race` | Run short package tests with the race detector. |
| `build-check` | Build every Go package. |
| `guardrails` | Run formatting, vet, lint, tests, race tests, and build check. |
| `e2e-http` | Run the HTTP E2E suite. |
| `e2e-grpc` | Run the gRPC E2E suite. |
| `e2e-milter` | Run the Milter E2E suite. |
| `e2e-docker` | Check Docker/Compose runtime agreement. |
| `e2e` | Run all E2E targets. |
| `docker-build` | Build the main local image as `doppelgaenger`. |
| `docker-build-fake` | Build the fake HTTP image as `fakehttpserver`. |
| `docker-run` | Run the main image with only port 8443 published. It does not mount the required configuration or certificates, so prefer the documented explicit `docker run` command. |
| `sbom` | Generate `sbom.cdx.json` with CycloneDX. |

## Changing protocol behavior

Start from the client-visible guarantee and add a reproducer at the narrowest
layer. A change to HTTP rules, gRPC stream handling, or Milter reply turns often
needs both focused unit tests and the corresponding E2E update.

Review these coupled surfaces:

- configuration fields, defaults, normalization, and validation;
- Primary error behavior;
- Shadow timeout, queue, and failure isolation;
- structured log fields;
- Prometheus labels and OTLP spans/metrics;
- annotated configuration and operator documentation.

Do not describe a behavior as supported solely because a low-level codec can
parse it. Support claims need an active runtime path and suitable tests.

## Backend-product neutrality

Project-owned protocol behavior, defaults, examples, fixtures, tests, and
documentation must use neutral names and behavioral descriptions. Do not tie a
profile to a named backend product or rely on an external product image for
compatibility proof. Model the required wire behavior with a repository-owned
test double instead.

Names of implemented standards, protocols, dependencies, build tools, and
deployment interfaces remain explicit where operators need them. The rule is
against hidden backend-product coupling, not against accurately naming an API
or tool that the project actually uses.

## Documentation changes

Keep the root README short. Put task material under `docs/operator`, tutorials
under `docs/tutorials`, and implementation material under `docs/developer`.

Every operator-visible claim should be traceable to configuration code,
runtime code, or a test. Examples must use supported fields and explain whether
they are safe defaults, local-only shortcuts, or production choices.

When adding or removing a configuration key, update:

- the annotated root `config.yaml`;
- `config.docker.yaml` if the Compose shape is affected;
- the matching operator reference;
- focused configuration tests.

## Commit format

Use an approved capitalized prefix and concise headline, followed by a short
bullet-list body:

```text
Docs: Restructure operator documentation

- Keep the README focused on orientation
- Add verified protocol and deployment references
- Record the validation performed
```

Approved prefixes are listed in `AGENTS.md` and `POLICY.md`. Split unrelated
work when one prefix and headline cannot describe it honestly.
