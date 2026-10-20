# Remote GitHub Workflow Executor

This repository provides a reusable GitHub Action for a specific gap in GitHub Actions: triggering a workflow in a private repository from a workflow running in a public repository, then waiting for that remote workflow to finish.

The action dispatches a `workflow_dispatch` event to a target repository, discovers the resulting run, polls until completion, and returns the remote run metadata to the caller.

## Problem It Solves

GitHub does not provide a simple built-in pattern for a public repository workflow to orchestrate a workflow hosted in a private repository. This action handles that flow by:

- triggering a workflow in the target repository
- locating the created workflow run
- monitoring the run to completion
- failing the caller job if the remote workflow fails
- exposing the remote run ID, URL, and conclusion as outputs
- collecting structured data the remote workflow returns, and exposing it as a JSON output

## Usage

A complete, runnable end-to-end example lives in
[`.github/workflows/example-submit-manifest.yaml`](.github/workflows/example-submit-manifest.yaml):
it dispatches a private workflow, waits for it, and reads structured data back.

```yaml
steps:
	- name: Execute remote GitHub workflow
		id: remote_executor
		uses: canonical/why-is-this-not-a-feature/.github/actions/remote_github_workflow_executor@main
		with:
			github_token: ${{ secrets.GH_TOKEN }}
			target_repo_owner: canonical
			target_repo_name: insert-private-repo-name
			target_workflow: 123456789 # obtained by running `gh workflow list` in the target repository
			target_ref: main # the branch or tag to dispatch in the target repository, defaults to `main`
			workflow_inputs_json: | 
				{
					"product": "${{ inputs.product }}",
					"cluster": "${{ inputs.cluster }}",
					"type": "${{ inputs.type }}",
				}
```

## Inputs

| Input | Required | Description |
| --- | --- | --- |
| `github_token` | Yes | GitHub token with permission to dispatch and read workflow runs in the target repository. |
| `target_repo_owner` | Yes | Owner of the target repository. |
| `target_repo_name` | Yes | Name of the target repository. |
| `target_workflow` | Yes | Workflow ID or workflow file name in the target repository. |
| `target_ref` | No | Git ref to dispatch in the target repository. Defaults to `main`. |
| `workflow_inputs_json` | No | JSON object string passed as `workflow_dispatch` inputs. Defaults to `{}`. |
| `metadata` | No | Arbitrary JSON metadata to pass to the remote workflow. Merged with auto-collected caller context (see below). Defaults to `{}`. |
| `collect_outputs` | No | Whether to collect structured data returned by the remote workflow. Defaults to `true`. |
| `outputs_artifact` | No | Artifact name the remote workflow uploads its outputs JSON in. Defaults to `workflow-outputs`. |
| `outputs_step_prefix` | No | Step-name prefix for the inline fallback channel. Defaults to `outputs::`. |

## Outputs

| Output | Description |
| --- | --- |
| `run_id` | ID of the triggered remote workflow run. |
| `run_url` | URL of the triggered remote workflow run. |
| `conclusion` | Final conclusion reported by the remote workflow run. |
| `remote_outputs` | JSON object of data returned by the remote workflow. `{}` when it returned none. |
| `remote_outputs_source` | Where the data came from: `artifact`, `step`, or `none`. |

Example output usage:

```yaml
- name: Report remote run
	run: |
		echo "Remote run ID: ${{ steps.remote_executor.outputs.run_id }}"
		echo "Remote run URL: ${{ steps.remote_executor.outputs.run_url }}"
		echo "Conclusion: ${{ steps.remote_executor.outputs.conclusion }}"

- name: Use data returned by the remote workflow
	env:
		ARTEFACT_URL: ${{ fromJSON(steps.remote_executor.outputs.remote_outputs).artefact_url }}
	run: echo "Dashboard: ${ARTEFACT_URL}"
```

## Requirements

- The target workflow must support `workflow_dispatch`.
- The token supplied in `github_token` must be able to dispatch workflows and read workflow runs in the private target repository. Collecting outputs additionally needs `actions: read` on that repository.
- If you run this on self-hosted runners, Go must be available because the composite action executes small Go programs with `go run`.

## Behavior

At runtime the action:

1. logs the invocation details
2. dispatches the target workflow via the GitHub REST API
3. waits briefly for the run to register
4. finds the newly created run ID
5. polls the run until it completes
6. collects any data the remote workflow returned
7. returns outputs and fails if the remote conclusion is not `success`

The caller job will fail when the remote workflow finishes with any conclusion other than `success`.

## Notes

- `target_workflow` can be either a workflow ID or a workflow filename such as `deploy.yml`.
- `workflow_inputs_json` must be valid JSON.
- For stable consumption, pin the action to a tag or commit SHA instead of a moving branch when you are ready to productionize it.

## Returning Data From The Remote Workflow

GitHub's REST API exposes neither job outputs nor step summaries for a dispatched run, so a remote workflow cannot simply declare `outputs:` and have the caller read them. Two channels actually cross the repository boundary, and the action reads both — artifact first, step name second.

### Preferred: upload an artifact

Have the target workflow write a JSON object and upload it under the name the caller passes as `outputs_artifact` (default `workflow-outputs`):

```yaml
- name: Publish workflow outputs
  if: always()
  run: |
    jq -n --arg url "$ARTEFACT_URL" --argjson ids "$IDS" \
      '{artefact_url: $url, test_execution_ids: $ids}' > outputs.json

- uses: actions/upload-artifact@v7
  with:
    name: workflow-outputs
    path: outputs.json
```

Arbitrary size, structured, and unaffected by UI changes. If the archive holds several JSON files, `outputs.json` wins; otherwise the first `.json` file is used.

### Fallback: encode the payload in a step name

When adding an upload step isn't practical, the target workflow can interpolate the payload into a **step name**, which the jobs API does return:

```yaml
- name: "outputs::{\"artefact_id\":${{ steps.submit.outputs.artefact_id }}}"
  run: ':'
```

The prefix is configurable via `outputs_step_prefix`. The last matching step wins. This channel is length-limited and makes the step name machine-shaped, so prefer the artifact.

### Consuming the result

Either way the caller gets one JSON object:

```yaml
- name: Use the returned data
  env:
    OUTPUTS: ${{ steps.remote_executor.outputs.remote_outputs }}
  run: echo "$OUTPUTS" | jq -r .artefact_url
```

Collection never fails the caller. If the remote workflow returned nothing, or the lookup itself errored, `remote_outputs` is `{}` and `remote_outputs_source` is `none` — the remote run's own conclusion still decides whether the job passes. Collection also runs when the remote workflow *failed*, so partial data such as a dashboard link is still available for triage.

## Metadata

The action automatically collects caller context and merges it with any caller-provided `metadata` input. The merged JSON is injected as the `metadata` key in `workflow_inputs_json` before dispatching.

> **Note:** Because GitHub `workflow_dispatch` inputs are always strings, `metadata` arrives in the target workflow as a **JSON-encoded string**, not a parsed object. The target workflow must parse it to access individual fields, e.g.:
> ```yaml
> - run: echo '${{ inputs.metadata }}' | jq .caller_repo
> ```

Auto-collected fields:

| Key | Value |
| --- | --- |
| `source` | Full URL to the caller workflow run (e.g. `https://github.com/org/repo/actions/runs/123/attempts/1`) |
| `caller_repo` | Caller repository (`owner/name`) |
| `caller_actor` | GitHub user or app that triggered the caller workflow |
| `caller_sha` | Commit SHA of the caller workflow |
| `caller_ref` | Git ref of the caller workflow |
| `caller_workflow` | Name of the caller workflow |
| `caller_event` | Event that triggered the caller workflow |

Caller-provided metadata (via the `metadata` input) is deep-merged on top, so you can override any auto-collected field or add your own keys.

**It is up to the consuming (target) repository to handle the `metadata` input.** If the target workflow does not declare a `metadata` input, the GitHub API will reject the dispatch with HTTP 422 ("Unexpected inputs provided"). The action handles this gracefully: when a 422 is detected for unexpected inputs, it automatically strips the `metadata` key and retries the dispatch without it. A warning is logged but the workflow proceeds normally. Once the target workflow adds a `metadata` input, the fallback stops firing and metadata flows through automatically.