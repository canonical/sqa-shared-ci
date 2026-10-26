#!/usr/bin/env bash
# Copyright 2026 Canonical Ltd.
# See LICENSE file for licensing details.
set -euo pipefail

home="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
export CIT_HOME="$temporary/runner"
export CIT_WORKDIR="$temporary/work"
export CIT_SUITE_DIR="$CIT_WORKDIR/suite"
export CIT_SKIP_PROVISION=true
export CIT_TEST_CALLS="$temporary/calls"
export CIT_TEST_BUILD_STATUS=0 CIT_TEST_STATUS=0
unset CIT_CHARM_FILE CIT_CONFIG
mkdir -p "$CIT_HOME" "$temporary/bin" "$temporary/charm source"
cp -R "$home/scripts" "$CIT_HOME/scripts"
cp "$home/versions.env" "$CIT_HOME/"
source_dir="$temporary/charm source"
printf 'name: test-charm\n' > "$source_dir/charmcraft.yaml"
printf 'stale\n' > "$source_dir/stale_amd64.charm"
printf '{}\n' > "$source_dir/charm-integration-testing.json"

# Provisioning is deliberately never run by these contract tests.
printf '#!/usr/bin/env bash\nexit 0\n' > "$CIT_HOME/scripts/install.sh"
cat > "$temporary/bin/charmcraft" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit_status="${CIT_TEST_BUILD_STATUS:?}"
((exit_status == 0)) || exit "$exit_status"
output=.
while (($#)); do
    if [[ "$1" == --output ]]; then
        output="$2"
        shift
    fi
    shift
done
printf 'fresh\n' > "$output/fresh_amd64.charm"
EOF
cat > "$temporary/bin/poetry" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '<%s>\n' "$@" >> "$CIT_TEST_CALLS"
exit "$CIT_TEST_STATUS"
EOF
cat > "$temporary/bin/getent" <<'EOF'
#!/usr/bin/env bash
exit 2
EOF
chmod +x "$temporary/bin/"* "$CIT_HOME/scripts/install.sh"
export PATH="$temporary/bin:$PATH"

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

run_ci() {
    bash "$CIT_HOME/scripts/ci.sh" "$source_dir" machine --plan
}

if [[ "${1:-}" == build-failure ]]; then
    export CIT_TEST_BUILD_STATUS=23
    status=0
    run_ci || status=$?
    [[ "$status" == 23 ]] || fail "build exit status lost: expected 23, got $status"
    [[ ! -e "$CIT_TEST_CALLS" ]] || fail "tests ran after a failed build"
    printf 'PASS: build failure preserves exit status and does not run tests\n'
    exit
fi

artifacts="$(bash "$CIT_HOME/scripts/build.sh" "$source_dir")"
[[ "$artifacts" != *stale* ]] || fail "selected a stale artifact from the source directory"
[[ -f "$artifacts" ]] || fail "expected exactly one fresh artifact, got: $artifacts"
printf 'PASS: build selects only fresh artifacts\n'

run_ci
grep -Fxq "<$source_dir/charm-integration-testing.json>" "$CIT_TEST_CALLS" \
    || fail "configuration path containing spaces was not preserved"
grep -Fxq '<--plan>' "$CIT_TEST_CALLS" || fail "extra runner arguments were not forwarded"
printf 'PASS: CI builds and forwards configuration and runner arguments\n'

export CIT_TEST_STATUS=17
status=0
run_ci || status=$?
[[ "$status" == 17 ]] || fail "test exit status lost: expected 17, got $status"
printf 'PASS: test failures fail CI\n'

export CIT_TEST_STATUS=0 CIT_CHARM_FILE="$artifacts" CIT_TEST_BUILD_STATUS=23
run_ci
printf 'PASS: prebuilt charms bypass builds\n'

just --justfile "$home/justfile" plan "$artifacts" machine --config "$source_dir/charm-integration-testing.json"
printf 'PASS: Just recipes forward arguments containing spaces\n'

bash "$home/scripts/init.sh" "$source_dir" --github
[[ -f "$source_dir/.github/workflows/charm-integration-tests.yaml" ]] || fail "workflow was not bootstrapped"
printf '{"neighbors":["custom"]}\n' > "$source_dir/charm-integration-testing.json"
bash "$home/scripts/init.sh" "$source_dir" --github
[[ "$(cat "$source_dir/charm-integration-testing.json")" == '{"neighbors":["custom"]}' ]] \
    || fail "bootstrap overwrote existing configuration"
printf 'PASS: bootstrap is idempotent and preserves consumer configuration\n'

# shellcheck source=../scripts/lib.sh
source "$home/scripts/lib.sh"
flaky_calls=0
flaky() {
    flaky_calls=$((flaky_calls + 1))
    ((flaky_calls == 3))
}
sleep() { printf '%s\n' "$1" >> "$temporary/delays"; }
export CIT_RETRY_DELAY=2
retry 3 flaky
[[ "$(cat "$temporary/delays")" == $'2\n4' ]] || fail "retry did not use exponential backoff"
printf 'PASS: retries are bounded and use exponential backoff\n'

flaky_calls=0
if retry 2 flaky; then
    fail "retry exceeded the attempt limit"
fi
[[ "$flaky_calls" == 2 ]] || fail "expected exactly two attempts"
printf 'PASS: exhausted retries fail explicitly\n'
