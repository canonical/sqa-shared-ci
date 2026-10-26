#!/usr/bin/env bash
# Bootstrap charm integration testing in a charm repository.
#
# Usage: init.sh <charm-repo-dir> [--github]
# Writes charm-integration-testing.json (empty: discover and run every applicable scenario).
# With --github, also writes a GitHub Actions workflow that calls the shared action.
# Existing files are never overwritten.
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

repo="${1:?Usage: init.sh <charm-repo-dir> [--github]}"
github="${2:-}"
[[ -z "$github" || "$github" == --github ]] || die "Unknown option: $github"
[[ -f "$repo/charmcraft.yaml" ]] || die "$repo does not contain charmcraft.yaml"

install_template() {
    local template="$1" destination="$2"
    if [[ -e "$destination" ]]; then
        log "Keeping existing $destination"
        return
    fi
    mkdir -p "$(dirname "$destination")"
    cp "$CIT_HOME/templates/$template" "$destination"
    log "Created $destination"
}

install_template charm-integration-testing.json "$repo/charm-integration-testing.json"
if [[ -n "$github" ]]; then
    install_template github-workflow.yaml "$repo/.github/workflows/charm-integration-tests.yaml"
else
    cat >&2 <<EOF
Run from any CI provider on a disposable Ubuntu host:
  git clone --depth 1 https://github.com/canonical/sqa-shared-ci
  sqa-shared-ci/charm-integration-testing/scripts/ci.sh . <kubernetes|machine>
EOF
fi
