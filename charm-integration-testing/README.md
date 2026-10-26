# Charm Integration Testing

Build a charm from source and run the
[charm-integration-testing](https://github.com/canonical/charm-integration-testing) suite against it.
Everything is plain bash so it runs on any CI provider or locally; the GitHub action is a thin wrapper.

By default every integration endpoint of the charm is paired with every compatible neighbor found on
Charmhub, and the full suite runs for each pairing. The charm under test is always the locally built
artifact. Neighbor charms come from Charmhub; metadata lookups and tool installs retry with exponential
backoff.

## Quick start

On a disposable Ubuntu host (CI runner or VM), from your charm repository:

```bash
git clone --depth 1 https://github.com/canonical/sqa-shared-ci
sqa-shared-ci/charm-integration-testing/scripts/init.sh .            # add --github for a workflow
sqa-shared-ci/charm-integration-testing/scripts/ci.sh . kubernetes   # or: machine
```

`init.sh` writes an empty `charm-integration-testing.json` (run everything) and, with `--github`,
`.github/workflows/charm-integration-tests.yaml`, which uses the action:

```yaml
- uses: canonical/sqa-shared-ci/.github/actions/charm_integration_tests@main
  with:
    platform: kubernetes
```

`ci.sh` modifies the host (snaps, LXD, Canonical k8s, Chaos Mesh, firewall rules). Do not run it on a
shared machine.

## Scripts

| Script | Purpose |
| --- | --- |
| `scripts/ci.sh <src> <platform> [args]` | `install`, `provision`, `build`, and `test` in one step |
| `scripts/install.sh` | Install juju, charmcraft, log tools, and the pinned suite into `$CIT_WORKDIR` |
| `scripts/provision.sh <platform>` | Provision LXD (always; charmcraft needs it) and, for `kubernetes`, Canonical k8s as `local-k8s` |
| `scripts/build.sh <src>` | `charmcraft pack` and print the artifact paths |
| `scripts/test.sh <charm> <platform> [args]` | Run the suite's `ci_runner` against a packed charm |
| `scripts/init.sh <repo> [--github]` | Bootstrap a charm repository |

Extra arguments go to the suite's `ci_runner`, e.g. `--plan` to only list the discovered scenarios.
The [justfile](justfile) offers the same steps for local use, e.g.
`just -f sqa-shared-ci/charm-integration-testing/justfile plan ./my.charm kubernetes`.
Builds use a fresh output directory so existing `.charm` files cannot be selected accidentally.
Build failures stop the pipeline immediately; failed tests are never retried wholesale.

Run the infrastructure-free shell contract tests with
`just -f charm-integration-testing/justfile check` and lint with
`just -f charm-integration-testing/justfile lint`. The tests use fake external tools; they do not
prove that LXD, Kubernetes, Charmcraft, or a live charm deployment works.

## Configuration

`charm-integration-testing.json` in the charm directory (or `CIT_CONFIG`) narrows or replaces scenario
discovery, supplies resources and suite overrides, and adds pytest arguments:

```json
{
  "endpoints": ["database"],
  "neighbors": ["postgresql-k8s"],
  "pytest_args": ["-k", "not test_upgrade_controller"],
  "overrides": "./charm-overrides"
}
```

Explicit `scenarios` replace discovery entirely. OCI image resources default to the `upstream-source`
in the charm metadata. See the suite's
[local charm CI guide](https://github.com/canonical/charm-integration-testing/blob/main/docs/how-to/local-charm-ci.rst)
for every field.

| Variable | Default | Purpose |
| --- | --- | --- |
| `CIT_WORKDIR` | `~/charm-integration-testing` | Suite checkout, artifacts, results; must be a non-hidden path under `$HOME` for strictly confined Juju |
| `CIT_REF` / `CIT_REPO` | see [versions.env](versions.env) | Suite version to test with |
| `CIT_CHARM_FILE` | built from source | Test a prebuilt `.charm` |
| `CIT_SKIP_PROVISION` | `false` | Use existing clouds instead of provisioning |
| `CIT_CLOUD` | `local-k8s` / `localhost` | Juju cloud to bootstrap on |
| `KUBECONFIG_<cloud>` | provisioned kubeconfig | Kubeconfig per Kubernetes cloud, e.g. `KUBECONFIG_my_k8s` |
| `CIT_OUTPUT_DIR` | `$CIT_WORKDIR/results/<time>` | Per-scenario JUnit reports, bundles, and logs |
| `CHARMCRAFT_PACK_ARGS` | none | Extra `charmcraft pack` arguments |

## Test Observer reporting (optional)

Create a Test Observer execution, then export `TEST_OBSERVER_API`, `TEST_OBSERVER_TOKEN`, and
`TEST_OBSERVER_EXECUTION_ID` (or set the matching action inputs). Results for every scenario are
posted to that execution; reporting failures fail the run. The caller owns the execution's lifecycle.
