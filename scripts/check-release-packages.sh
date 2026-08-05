#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

repo_root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_root"

fail() {
	printf 'check-release-packages: %s\n' "$*" >&2
	exit 1
}

require_file() {
	[[ -f "$1" ]] || fail "missing required file: $1"
}

require_literal() {
	grep -F -- "$2" "$1" >/dev/null || fail "$3"
}

workflow=.github/workflows/build-stable.yaml
require_file "$workflow"
require_literal "$workflow" 'build-linux-packages:' \
	'the release workflow must include a native-package job'
require_literal "$workflow" 'jiro4989/build-deb-action@a883c65147d80579cb359548b9a902ff0a35ae5b' \
	'the Debian package action must match the SHA-pinned sibling-repository template'
require_literal "$workflow" 'jiro4989/build-rpm-action@f11474937f502aaa8bb36d8c1a8ec6f8de536a0c' \
	'the RPM package action must match the SHA-pinned sibling-repository template'
require_literal "$workflow" 'package_root: .debpkg' \
	'the release workflow must populate a Debian package root'
require_literal "$workflow" 'package_root: .rpmpkg' \
	'the release workflow must populate an RPM package root'
require_literal "$workflow" 'version: ${{ steps.release_metadata.outputs.package_version }}' \
	'native package versions must come from validated release metadata'
require_literal "$workflow" '--file "$pkg"' \
	'the release workflow must generate an SBOM for each native package'
require_literal "$workflow" 'sha256sum "$pkg"' \
	'the release workflow must generate a SHA-256 checksum for each native package'
require_literal "$workflow" 'safe_pkg="${pkg//\~/.}"' \
	'the release workflow must normalize package asset names before checksumming'
require_literal "$workflow" 'build_rpm: false' \
	'the workflow must explicitly limit RPM output to the verified x86_64 template'
for extension in deb rpm; do
	require_literal "$workflow" "./*.${extension}" \
		"the release workflow must upload .${extension} packages"
	require_literal "$workflow" "./*.${extension}.sha256" \
		"the release workflow must upload .${extension} checksums"
done

require_literal scripts/sbom.sh '--file' \
	'the SBOM helper must support scanning native package files'

package_version="$(./scripts/release-semver-metadata.sh v1.0.0-beta.10 | awk -F= '$1 == "package_version" { print $2 }')"
[[ "$package_version" == '1.0.0~beta.10' ]] || \
	fail "prerelease package version is not Debian/RPM compatible: $package_version"

printf 'check-release-packages: native release package contracts are present\n'
