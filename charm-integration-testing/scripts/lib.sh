#!/usr/bin/env bash
# Shared helpers for the charm integration testing scripts. Source, do not execute.

CIT_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export CIT_HOME
# shellcheck source=../versions.env
source "$CIT_HOME/versions.env"

# Strictly confined Juju can only read non-hidden paths under $HOME.
CIT_WORKDIR="${CIT_WORKDIR:-$HOME/charm-integration-testing}"
CIT_SUITE_DIR="${CIT_SUITE_DIR:-$CIT_WORKDIR/suite}"
export CIT_WORKDIR CIT_SUITE_DIR

log() {
    printf '==> %s\n' "$*" >&2
}

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

# retry ATTEMPTS COMMAND... runs COMMAND with exponential backoff (CIT_RETRY_DELAY seconds, doubling).
retry() {
    local attempts="$1" delay="${CIT_RETRY_DELAY:-5}" attempt=1
    shift
    until "$@"; do
        if ((attempt >= attempts)); then
            log "Giving up after $attempt attempts: $*"
            return 1
        fi
        log "Attempt $attempt/$attempts failed; retrying in ${delay}s: $*"
        sleep "$delay"
        attempt=$((attempt + 1))
        delay=$((delay * 2))
    done
}

snap_install() {
    local name="$1"
    shift
    if snap list "$name" >/dev/null 2>&1; then
        log "$name snap already installed"
        return
    fi
    retry 5 sudo snap install "$name" "$@"
}

# Runs a command with LXD socket access; new group membership is not active in the current session.
with_lxd_group() {
    if ! getent group lxd >/dev/null || id -nG | tr ' ' '\n' | grep -qx lxd || [[ "$(id -u)" == 0 ]]; then
        "$@"
    else
        sg lxd -c "$(printf '%q ' "$@")"
    fi
}

require_platform() {
    case "${1:-}" in
        kubernetes | machine) ;;
        *) die "Platform must be 'kubernetes' or 'machine', got '${1:-}'" ;;
    esac
}
