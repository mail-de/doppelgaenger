#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ $# -ne 1 ]]; then
	printf 'Usage: %s <vMAJOR.MINOR.PATCH[-PRERELEASE]>\n' "$0" >&2
	exit 1
fi

tag="$1"
semver_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?$'

if [[ ! "$tag" =~ $semver_pattern ]]; then
	printf "Unsupported release tag '%s'. Expected vMAJOR.MINOR.PATCH[-PRERELEASE].\n" "$tag" >&2
	exit 1
fi

version="${tag#v}"
base_version="${version%%-*}"
base_tag="${tag%%-*}"
prerelease=false

if [[ "$version" == *-* ]]; then
	prerelease=true
fi

IFS='.' read -r major minor patch <<<"$base_version"

printf 'tag=%s\n' "$tag"
printf 'version=%s\n' "$version"
printf 'base_version=%s\n' "$base_version"
printf 'package_version=%s\n' "${version//-/\~}"
printf 'prerelease=%s\n' "$prerelease"
printf 'tag_major=v%s\n' "$major"
printf 'tag_minor=v%s.%s\n' "$major" "$minor"
printf 'tag_patch=%s\n' "$base_tag"
printf 'major=%s\n' "$major"
printf 'minor=%s\n' "$minor"
printf 'patch=%s\n' "$patch"
