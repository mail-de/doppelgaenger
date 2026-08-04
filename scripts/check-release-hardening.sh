#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

repo_root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root"

fail() {
	printf 'check-release-hardening: %s\n' "$*" >&2
	exit 1
}

required_files=(
	.github/workflows/guardrails.yaml
	.github/workflows/unit-tests.yaml
	.github/workflows/govulncheck-main.yaml
	.github/workflows/codeql.yml
	.github/workflows/docker-build-push.yaml
	.github/workflows/docker-features.yaml
	.github/workflows/docker-stable-build.yaml
	.github/workflows/docker-stable.yaml
	.github/workflows/docker-refresh-stable.yaml
	.github/workflows/build-stable.yaml
	scripts/docker-base-digests.sh
	scripts/docker-stable-refresh-check.sh
	scripts/release-semver-metadata.sh
	scripts/sbom.sh
)

for path in "${required_files[@]}"; do
	[[ -f "$path" ]] || fail "missing required file: $path"
done

if grep -R -n -E '(curl|wget)[^|]*\|[[:space:]]*(sh|bash)\b' \
	--exclude='check-release-hardening.sh' scripts .github/workflows; then
	fail "remote installer bytes must not be piped directly into a shell"
fi

grep -F 'checksums.txt' scripts/sbom.sh >/dev/null || \
	fail "scripts/sbom.sh must verify the Syft checksum manifest"
grep -Eq '(sha256sum|shasum -a 256)' scripts/sbom.sh || \
	fail "scripts/sbom.sh must use a SHA-256 verifier"

unpinned_actions="$({
	grep -R -h -E '^[[:space:]]+uses:[[:space:]]+[^.]' .github/workflows || true
} | grep -Ev '@[0-9a-f]{40}([[:space:]]+#.*)?$' || true)"
if [[ -n "$unpinned_actions" ]]; then
	printf '%s\n' "$unpinned_actions" >&2
	fail "all third-party GitHub Actions must be pinned to a full commit SHA"
fi

# GitHub expressions in these checks must remain literal.
# shellcheck disable=SC2016
grep -F 'GO_IMAGE=${{ env.GO_IMAGE }}@${{ steps.base_digests.outputs.golang_digest }}' .github/workflows/docker-stable-build.yaml >/dev/null || \
	fail "stable Docker builds must pin the Go builder by digest"
# shellcheck disable=SC2016
grep -F 'CERTS_IMAGE=${{ env.CERTS_IMAGE }}@${{ steps.base_digests.outputs.certs_digest }}' .github/workflows/docker-stable-build.yaml >/dev/null || \
	fail "stable Docker builds must pin the certificate stage by digest"
grep -F 'sbom: true' .github/workflows/docker-stable-build.yaml >/dev/null || \
	fail "stable Docker builds must publish an SBOM attestation"
grep -F 'provenance: mode=max' .github/workflows/docker-stable-build.yaml >/dev/null || \
	fail "stable Docker builds must publish maximum provenance"
grep -F 'actions/attest-build-provenance@' .github/workflows/build-stable.yaml >/dev/null || \
	fail "release artifacts must receive build provenance attestations"
grep -F "if: vars.ENABLE_GITHUB_ATTESTATIONS == 'true'" .github/workflows/build-stable.yaml >/dev/null || \
	fail "private-repository artifact attestations must be explicitly gated"
grep -F "if: vars.ENABLE_CODEQL == 'true'" .github/workflows/codeql.yml >/dev/null || \
	fail "private-repository CodeQL must be explicitly gated"

grep -Eq '^[[:space:]]{2}govulncheck:' .github/workflows/build-stable.yaml || \
	fail "release artifacts must be gated by govulncheck"
grep -Eq '^[[:space:]]{2}govulncheck:' .github/workflows/docker-stable.yaml || \
	fail "stable images must be gated by govulncheck"
grep -F 'release-guardrails: guardrails govulncheck' Makefile >/dev/null || \
	fail "Makefile release guardrails must include govulncheck"

printf 'check-release-hardening: release supply-chain guardrails are present\n'
