# Security maintenance review — 2026-09-07

## Checkout and scope

Verified checkout: `mail-de/doppelgaenger`, origin
`git@github.com:mail-de/doppelgaenger.git`, branch `main`, starting commit
`cd15066bfa0de18b16abb6ab83dc930ff62b945b`. The working tree was clean and the
GitHub main commit matched. No unrelated changes were present.
The supplied export was treated as untrusted, stale evidence. Its snapshot
also predates its own Trivy completion timestamp. Findings were independently
checked against current source, official advisories and fresh scanner data.

## Finding disposition

“Fixed” below means the working source/dependency tree, not a released artifact
or deployed process. The published beta has not been replaced.

| Finding | Disposition and evidence | Original source |
| --- | --- | --- |
| CVE-2026-56854 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-6303](https://vuln.go.dev/ID/GO-2026-6303.json) |
| CVE-2026-39828 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5014](https://vuln.go.dev/ID/GO-2026-5014.json) |
| CVE-2026-39829 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5018](https://vuln.go.dev/ID/GO-2026-5018.json) |
| CVE-2026-39830 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5017](https://vuln.go.dev/ID/GO-2026-5017.json) |
| CVE-2026-39831 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5019](https://vuln.go.dev/ID/GO-2026-5019.json) |
| CVE-2026-39832 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5006](https://vuln.go.dev/ID/GO-2026-5006.json) |
| CVE-2026-39835 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5015](https://vuln.go.dev/ID/GO-2026-5015.json) |
| CVE-2026-42508 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5021](https://vuln.go.dev/ID/GO-2026-5021.json) |
| CVE-2026-46595 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5023](https://vuln.go.dev/ID/GO-2026-5023.json) |
| CVE-2026-46597 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5013](https://vuln.go.dev/ID/GO-2026-5013.json) |
| CVE-2026-46600 | Module version fixed by x/net v0.57.0. External dnsmessage is not imported; the standard-library copy is used. Go 1.26.5 builds were affected by the same advisory; Go 1.27.1 fixes that build baseline. | [GO-2026-5942](https://vuln.go.dev/ID/GO-2026-5942.json) |
| CVE-2026-84304 | gRPC transport is used by `internal/grpcproxy/server.go`. Fixed by vendoring v1.83.1; receive buffer compaction is enabled by default. | [Maintainer advisory](https://github.com/grpc/grpc-go/security/advisories/GHSA-vp52-pcj8-j9qc) |
| CVE-2026-39827 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5016](https://vuln.go.dev/ID/GO-2026-5016.json) |
| CVE-2026-39833 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5005](https://vuln.go.dev/ID/GO-2026-5005.json) |
| CVE-2026-39834 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5020](https://vuln.go.dev/ID/GO-2026-5020.json) |
| CVE-2026-46598 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-5033](https://vuln.go.dev/ID/GO-2026-5033.json) |
| CVE-2026-56855 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-6355](https://vuln.go.dev/ID/GO-2026-6355.json) |
| CVE-2026-78662 | Module version fixed by x/crypto v0.56.0; affected SSH packages are neither vendored nor imported by the application. | [GO-2026-6354](https://vuln.go.dev/ID/GO-2026-6354.json) |
| GO-2026-5932 | Not applicable to the application: OpenPGP is not vendored or imported. Still reported at module level with no fixed version; no scanner suppression added. | [Go advisory](https://vuln.go.dev/ID/GO-2026-5932.json) |
| DS-0002, both vendor Dockerfiles | Not applicable to shipped images. The go-toml Dockerfile packages upstream conversion tools; the OpenTelemetry file inventories upstream development images. Neither is used by project builds. Project runtime stages use UID/GID 10001:10001. | [Trivy rule](https://github.com/aquasecurity/trivy-checks/blob/main/checks/docker/root_user.rego) |
| DS-0026, both vendor Dockerfiles | Not applicable to shipped images for the same build-path reason; retained as upstream files. | [Trivy rule](https://github.com/aquasecurity/trivy-checks/blob/main/checks/docker/no_healthcheck_instruction.rego) |
| DS-0026, Dockerfile and Dockerfile.faker | Still affected: no embedded HEALTHCHECK. Documented external probes for the configured readiness endpoint. Scratch has no probe client and proxy observability is opt-in; a version-only probe would not establish readiness. No suppression or dummy probe added. | [Trivy rule](https://github.com/aquasecurity/trivy-checks/blob/main/checks/docker/no_healthcheck_instruction.rego) |
| CI run 30947316605 | Already fixed in current source: obsolete lint action passed `run --version`, which failed. Current workflow installs lint explicitly and calls `make guardrails`. Latest main Guardrails run 30996370564 passed on the starting commit. | [Failed run](https://github.com/mail-de/doppelgaenger/actions/runs/30947316605), [replacement run](https://github.com/mail-de/doppelgaenger/actions/runs/30996370564) |
| Go 1.26.5 baseline | Fixed in source: Go 1.27.1 across go.mod, both Dockerfiles, Makefile, workflow pins, digest helper, packaging checks and documentation. Official release metadata and the amd64/arm64 Alpine builder tag were checked. | [Go releases](https://go.dev/dl/?mode=json) |

The dependency update also requires x/sys v0.47.0 and x/text v0.41.0.
`go mod tidy`, `go mod vendor` and `go mod verify` were run. Regeneration
restores upstream whitespace in four unchanged-version vendor files
(go-toml README/test script, pflag golangflag.go, zap CHANGELOG); these are not
handwritten dependency patches.

`GOENV=greenteagc` incorrectly named a Go environment configuration file.
Makefile and containers now match CI's `GOEXPERIMENT=runtimesecret`.
The packaging regression check failed on the old assignment before the fix.
Lint is pinned to v2.13.1 and govulncheck to v1.7.0 to match local validation.
Protocol production code was not changed. The gRPC fix is upstream transport
code; existing project protocol and stream tests exercise compatibility.

## Scanner coverage

Before updating, `make govulncheck` passed at package level while reporting
18 module-level vulnerabilities. This was not proof of safety: the Go database
module index did not yet include CVE-2026-84304. The gRPC maintainer advisory
independently confirmed the affected range <=1.83.0 and fixed version 1.83.1.
The updated vendored transport contains the default-enabled compaction fix.

After updating, govulncheck reports zero package vulnerabilities and one module
advisory (unused OpenPGP). Trivy was repeated with a newly created cache;
its database was updated at 2026-09-07T19:06:01Z and downloaded at
2026-09-07T21:39:14Z. All 18 supplied CVEs disappear. Remaining findings are
GO-2026-5932 and the six Docker rule findings classified above.
Original exported scanner reports were not supplied; the fresh run replaces
that missing evidence. No secrets scan was requested or performed.

GitHub reports zero open issues and zero open Dependabot alerts. These are
coverage observations, not safety claims. CodeQL is skipped under the
repository's documented licensing gate. No hosted CI run exists for these
uncommitted changes.

## Release readiness

Proposed next version: **v1.0.0-beta.11**. Retain beta status because this change
updates the toolchain and network transport, and no deployment stabilization
window has been observed. Age alone does not justify stable 1.0.0.

The live latest release remains v1.0.0-beta.10, published 2026-08-04T21:26:25Z;
main is two commits ahead before this maintenance. Remote release metadata
includes Linux archives and native package/SBOM/checksum assets. Their contents
and installation behavior were not revalidated in this source review.

Remaining release work: review and commit the changes, obtain hosted CI on that
commit, build/verify release archives and native packages on Linux amd64/arm64,
validate multi-platform images and attestations, and observe real deployment
readiness and Primary/Shadow behavior. Release artifacts and deployed versions
remain unverified and may still contain the old dependencies. Local Docker
smoke validation is not production rollout proof. Publishing, tagging, pushing
and deployment require separate authorization.

## Validation

- PASS: `make guardrails` with Go 1.27.1, runtimesecret and golangci-lint
  v2.13.1: packaging/release contracts, formatting, vet, full lint (zero issues),
  unit tests, race tests, HTTP/Milter/gRPC/Docker-contract E2E, build check.
- PASS: `make govulncheck` with v1.7.0; package scan findings described above.
- PASS: `GOOS=linux GOARCH=arm64 make build-check` after the E2E suites.
  An initial concurrent attempt encountered E2E-generated temporary Go source
  under `temp/go-tmp`; the sequential rerun passed. This is cross-compilation,
  not an arm64 runtime or multi-platform image test.
- PASS: `actionlint` after all workflow edits.
- PASS: `go mod verify`; separate `go mod vendor -o` output is byte-identical to
  the working vendor tree.
- PASS: `make docker-smoke docker-build-fake` with local audit image tags.
  Both Linux/amd64 images built; proxy version invocation passed with network
  disabled and a read-only root. Image readback confirms UID/GID 10001:10001.
- PASS: fresh-cache Trivy filesystem vulnerability/misconfiguration scan
  completed; residual findings are explicitly classified above.
- PASS: project-owned `git diff --check`. The full-tree check reports only
  whitespace restored from unchanged upstream vendor modules; canonical vendor
  content was retained rather than hand-edited.

Logs and scanner JSON are under `/tmp/doppelgaenger-audit/` in this workstation
session. They are temporary evidence, not published release attestations.

## Changed files

Module metadata: `go.mod`, `go.sum`, regenerated `vendor/` (crypto, net, sys,
text metadata, gRPC and canonical upstream whitespace).
Build surfaces: `Makefile`, `Dockerfile`, `Dockerfile.faker`,
`scripts/check-packaging.sh`, `scripts/docker-base-digests.sh`.
Workflow pins: `build-stable.yaml`, `codeql.yml`, `docker-stable-build.yaml`,
`docker-stable.yaml`, `govulncheck-main.yaml`, `guardrails.yaml`,
`unit-tests.yaml` under `.github/workflows/`.
Documentation: `README.md`, `docs/developer/development.md`,
`docs/developer/testing.md`, `docs/operator/deployment.md` and this report.
