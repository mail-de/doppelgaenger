#!/usr/bin/env bash
# Copyright (c) 2026 mail.de GmbH
# SPDX-License-Identifier: MIT

set -euo pipefail

git_root="$(git rev-parse --show-toplevel)"
remote_name="${1:-origin}"
remote_url="${2:-}"
zero_sha="0000000000000000000000000000000000000000"
triggered=0
target_commits=()
refs=()

while read -r _local_ref local_sha remote_ref _remote_sha; do
	[[ "$local_sha" != "$zero_sha" ]] || continue

	case "$remote_ref" in
		refs/heads/main | refs/tags/v*)
			triggered=1
			target_commit="$(git rev-parse "${local_sha}^{commit}")"
			target_commits+=("$target_commit")
			refs+=("${remote_ref} -> ${target_commit:0:12}")
			;;
	esac
done

if [[ "$triggered" -eq 0 ]]; then
	printf 'No release-sensitive refs in push; skipping govulncheck.\n'
	exit 0
fi

cd "$git_root"

if [[ -n "$(git status --porcelain)" ]]; then
	printf 'Push blocked: main and version tags require a clean checkout.\n' >&2
	exit 1
fi

head_commit="$(git rev-parse HEAD)"
for target_commit in "${target_commits[@]}"; do
	if [[ "$target_commit" != "$head_commit" ]]; then
		printf 'Push blocked: release-sensitive refs must point to the checked-out HEAD.\n' >&2
		exit 1
	fi
done

printf 'Running govulncheck before pushing release-sensitive refs to %s %s:\n' "$remote_name" "$remote_url"
printf '  - %s\n' "${refs[@]}"
make govulncheck
