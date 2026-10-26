#!/usr/bin/env bash
# Build a charm from source with charmcraft and print the packed artifact path(s).
#
# Usage: build.sh <charm-source-dir>
# Artifacts are copied to $CIT_WORKDIR/charms, where strictly confined Juju can read them.
# Set CHARMCRAFT_PACK_ARGS to pass extra arguments (for example "--platform ubuntu@24.04:amd64").
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

source_dir="$(cd "${1:?Usage: build.sh <charm-source-dir>}" && pwd)"
mkdir -p "$CIT_WORKDIR/charms"
output_dir="$(mktemp -d "$CIT_WORKDIR/charms/build-XXXXXXXX")"

read -r -a pack_args <<<"${CHARMCRAFT_PACK_ARGS:-}"
log "Packing charm in $source_dir"
(cd "$source_dir" && with_lxd_group charmcraft pack --verbose "${pack_args[@]}" --output "$output_dir") >&2

shopt -s nullglob
artifacts=("$output_dir"/*.charm)
((${#artifacts[@]} > 0)) || die "charmcraft produced no .charm files in $output_dir"
for artifact in "${artifacts[@]}"; do
    printf '%s\n' "$artifact"
done
