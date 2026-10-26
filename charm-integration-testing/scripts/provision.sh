#!/usr/bin/env bash
# Provision a disposable host with the clouds the integration suite needs.
#
# Usage: provision.sh <kubernetes|machine>
# LXD is always provisioned because charmcraft builds in LXD. Kubernetes additionally
# installs Canonical k8s and Chaos Mesh, registered with Juju as the "local-k8s" cloud.
# Do not run on shared hosts: this changes snaps, groups, and firewall rules.
set -euo pipefail

# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

platform="${1:-}"
require_platform "$platform"
[[ -d "$CIT_SUITE_DIR/development-sandbox/bin" ]] || die "Run install.sh before provision.sh"

export SANDBOX_USER
SANDBOX_USER="$(id -un)"
export KUBECONFIG_PATH="$CIT_WORKDIR/k8s.yaml"
sandbox_bin="$CIT_SUITE_DIR/development-sandbox/bin"

retry 3 bash "$sandbox_bin/setup-lxd.sh"
# Docker's default FORWARD policy drops LXD container traffic on hosts that also run Docker.
if sudo iptables --list DOCKER-USER >/dev/null 2>&1; then
    log "Allowing LXD traffic through Docker's firewall rules"
    sudo iptables --insert DOCKER-USER --jump ACCEPT
fi

if [[ "$platform" == kubernetes ]]; then
    retry 3 bash "$sandbox_bin/setup-k8s.sh"
fi
