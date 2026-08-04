# Releases and GitHub automation

The release workflow follows the same quality boundary as normal development:
no artifact or container is published before the relevant tests and
vulnerability gate pass.

## Branches and development images

`main` is the release-ready branch. `features` is the shared integration branch.
Pushes to either branch run Guardrails and Unit Tests. Pull requests targeting
either branch run the same checks.

The CodeQL workflow definition covers Go code plus GitHub Actions, but private
repositories in the current GitHub Free organization cannot upload code-scanning
results. The repository variable `ENABLE_CODEQL` therefore remains `false`.
Set it to `true` only after GitHub Code Security has been licensed and enabled
for this repository.

Every push to `features` publishes a multi-platform development image:

```text
ghcr.io/mail-de/doppelgaenger:dev
ghcr.io/mail-de/doppelgaenger:features
```

These mutable tags are for integration testing. Deployments that need
repeatability should pin the resulting manifest digest.

## Local release gate

Run the complete release gate from a clean checkout:

```sh
make release-guardrails
```

This includes static packaging and supply-chain checks, formatting, vet, lint,
unit tests, race tests, all protocol E2E suites, build checks, and
`govulncheck`. `make install-hooks` installs a pre-push hook that repeats
`govulncheck` for pushes to `main` and for `v*` tags.

## Release tags

Releases use annotated semantic-version tags:

```sh
git tag -a v1.2.3 -m "Release v1.2.3"
git push origin v1.2.3
```

Prereleases use a SemVer suffix such as `v1.2.3-rc.1`. The tag triggers three
independent gates:

1. Govulncheck validates reachable dependency vulnerabilities.
2. Release Build creates Linux archives for amd64 and arm64, SPDX JSON SBOMs,
   SHA-256 checksum files, and a GitHub Release.
3. Production Docker Build publishes an amd64/arm64 image index to GHCR with
   SBOM and maximum-provenance attestations.

The GitHub-native archive-attestation step is retained behind
`ENABLE_GITHUB_ATTESTATIONS`. It must remain `false` on the current private
GitHub Free repository and can be enabled if the repository moves to GitHub
Enterprise Cloud or becomes public. This plan limit does not disable the
BuildKit SBOM and provenance attached to GHCR images.

Stable releases publish the exact tag plus `latest`, `vMAJOR`, and
`vMAJOR.MINOR`. Prereleases publish only their exact tag. OCI metadata records
the source commit, source URL, build date, release tag, and the digests of the
Go and certificate-stage base images.

## Stable base-image refresh

The Stable Docker Refresh workflow checks the most recent non-prerelease image
daily. It compares the recorded Go-builder and certificate-stage digests with
the current upstream manifests. A change rebuilds the same release source and
adds an immutable `vX.Y.Z-rebuild-YYYYMMDD` tag while refreshing the stable
aliases. The workflow exits successfully without doing work until a stable
GitHub Release exists.

## Workflow inventory

| Workflow | Purpose |
| --- | --- |
| Guardrails | Canonical repository quality gate |
| Unit Tests | Fast independent package-test signal |
| Govulncheck Main Gate | Vulnerability gate for `main`, its pull requests, and release tags |
| CodeQL | Opt-in scheduled and change-driven Go/Actions analysis when GitHub Code Security is available |
| Development Docker Build | Multi-platform images from `features` |
| Production Docker Build | Vulnerability-gated images from `v*` tags |
| Stable Docker Refresh | Digest-aware rebuilds after base-image changes |
| Release Build | Archives, checksums, SBOMs, attestations, and GitHub Releases |

The reusable Docker workflows are internal implementation details shared by
the development, stable-release, and refresh entry points.
