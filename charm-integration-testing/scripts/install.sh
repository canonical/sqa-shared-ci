#!/usr/bin/env bash
# Install the integration suite and the tooling needed to build and test charms.
#
# Usage: install.sh
# Idempotent. Respects CIT_REPO/CIT_REF/CIT_SUITE_DIR and the channels in versions.env.
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

if ! command -v pipx >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
    log "Installing apt prerequisites"
    retry 5 sudo apt-get update
    retry 5 sudo apt-get install --yes git jq pipx
fi

snap_install juju --channel "$JUJU_CHANNEL"
snap_install charmcraft --classic --channel "$CHARMCRAFT_CHANNEL"
snap_install juju-crashdump --classic
snap_install kubectl --classic
# Strictly confined Juju does not create its data directory itself.
mkdir -p "$HOME/.local/share/juju"

export PATH="$HOME/.local/bin:$PATH"
if ! command -v poetry >/dev/null 2>&1; then
    retry 5 pipx install "poetry==$POETRY_VERSION"
fi
if ! command -v juju-k8s-crashdump >/dev/null 2>&1; then
    retry 5 pipx install "git+https://github.com/canonical/juju-k8s-crashdump.git@22ef04caaeeb94ad6ed49f8392b6bada65184569"
fi

if [[ ! -d "$CIT_SUITE_DIR/.git" ]]; then
    log "Cloning $CIT_REPO into $CIT_SUITE_DIR"
    mkdir -p "$CIT_SUITE_DIR"
    git -C "$CIT_SUITE_DIR" init --quiet
    git -C "$CIT_SUITE_DIR" remote add origin "$CIT_REPO"
fi
log "Checking out integration suite at $CIT_REF"
retry 5 git -C "$CIT_SUITE_DIR" fetch --quiet --depth 1 origin "$CIT_REF"
git -C "$CIT_SUITE_DIR" checkout --quiet --detach FETCH_HEAD

log "Installing integration suite dependencies"
retry 3 poetry --project "$CIT_SUITE_DIR" install --no-interaction
