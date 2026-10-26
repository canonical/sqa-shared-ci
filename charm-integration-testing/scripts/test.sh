#!/usr/bin/env bash
# Run the integration suite against a packed charm.
#
# Usage: test.sh <charm-file> <kubernetes|machine> [ci_runner arguments...]
# Extra arguments are passed to the suite's ci_runner, e.g. "--config charm-ci.json" or "--plan".
#
# Environment:
#   CIT_CLOUD                   Juju cloud (default: local-k8s or localhost, as provisioned)
#   CIT_OUTPUT_DIR              Results directory (default: $CIT_WORKDIR/results/<timestamp>)
#   KUBECONFIG_<cloud>          Kubeconfig for a Kubernetes cloud, e.g. KUBECONFIG_local_k8s
#   TEST_OBSERVER_API, TEST_OBSERVER_TOKEN, TEST_OBSERVER_EXECUTION_ID
#                               Optional Test Observer reporting
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

charm_file="${1:?Usage: test.sh <charm-file> <kubernetes|machine> [ci_runner arguments...]}"
platform="${2:-}"
require_platform "$platform"
shift 2
charm_file="$(cd "$(dirname "$charm_file")" && pwd)/$(basename "$charm_file")"
[[ -f "$charm_file" ]] || die "Charm file not found: $charm_file"
if [[ "$charm_file" != "$HOME"/* || "${charm_file#"$HOME"/}" == .* || "$charm_file" == */.*/* ]]; then
    log "Warning: strictly confined Juju may not read $charm_file; use a non-hidden path under \$HOME"
fi

if [[ "$platform" == kubernetes ]]; then
    cloud="${CIT_CLOUD:-local-k8s}"
    if [[ "$cloud" == local-k8s && -z "${KUBECONFIG_local_k8s:-}" ]]; then
        export KUBECONFIG_local_k8s="$CIT_WORKDIR/k8s.yaml"
    fi
else
    cloud="${CIT_CLOUD:-localhost}"
fi
output_dir="${CIT_OUTPUT_DIR:-$CIT_WORKDIR/results/$(date -u +%Y%m%dT%H%M%SZ)}"
export VALIDATORS_PATH="$CIT_SUITE_DIR/validators"
export PATH="$HOME/.local/bin:$PATH"

log "Testing $charm_file on $platform cloud $cloud; results in $output_dir"
with_lxd_group poetry --project "$CIT_SUITE_DIR" run python -m ci_runner \
    --charm-path "$charm_file" \
    --cloud "$cloud" \
    --platform "$platform" \
    --output-dir "$output_dir" \
    "$@"
