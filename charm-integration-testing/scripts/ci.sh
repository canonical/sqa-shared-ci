#!/usr/bin/env bash
# Install, provision, build, and test a charm in one step. This is the CI entry point.
#
# Usage: ci.sh <charm-source-dir> <kubernetes|machine> [ci_runner arguments...]
#
# Environment:
#   CIT_CONFIG          Scenario configuration (default: <charm-source-dir>/charm-integration-testing.json if present)
#   CIT_CHARM_FILE      Prebuilt .charm to test instead of building one
#   CIT_SKIP_PROVISION  Set to "true" to use existing clouds (see test.sh for CIT_CLOUD/KUBECONFIG_<cloud>)
# See test.sh for reporting and output options.
set -euo pipefail

scripts="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$scripts/lib.sh"

source_dir="${1:?Usage: ci.sh <charm-source-dir> <kubernetes|machine> [ci_runner arguments...]}"
platform="${2:-}"
require_platform "$platform"
shift 2

"$scripts/install.sh"
if [[ "${CIT_SKIP_PROVISION:-false}" != true ]]; then
    "$scripts/provision.sh" "$platform"
fi

charm_file="${CIT_CHARM_FILE:-}"
if [[ -z "$charm_file" ]]; then
    artifact_output="$("$scripts/build.sh" "$source_dir")"
    [[ -n "$artifact_output" ]] || die "Build produced no artifact paths"
    mapfile -t artifacts <<< "$artifact_output"
    if ((${#artifacts[@]} > 1)); then
        arch="$(dpkg --print-architecture)"
        mapfile -t artifacts < <(printf '%s\n' "${artifacts[@]}" | grep -- "$arch")
    fi
    ((${#artifacts[@]} == 1)) \
        || die "Expected one $platform artifact for this host, found: ${artifacts[*]:-none}; set CIT_CHARM_FILE"
    charm_file="${artifacts[0]}"
fi

config="${CIT_CONFIG:-$source_dir/charm-integration-testing.json}"
config_args=()
if [[ -n "${CIT_CONFIG:-}" || -f "$config" ]]; then
    config_args=(--config "$config")
fi

"$scripts/test.sh" "$charm_file" "$platform" "${config_args[@]}" "$@"
