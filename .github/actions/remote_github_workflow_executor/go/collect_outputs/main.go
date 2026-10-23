// Command collect_outputs pulls structured data back out of a completed remote
// workflow run.
//
// GitHub's REST API exposes neither job outputs nor step summaries for a
// dispatched run, so only two channels actually carry data across the
// repository boundary:
//
//  1. an artifact uploaded by the remote workflow (preferred — arbitrary size,
//     structured, stable), and
//  2. the name of a step, which the jobs API does return and which the remote
//     workflow can interpolate at runtime (fallback — no extra upload step, but
//     length-limited).
//
// Finding nothing is not an error: most remote workflows return no data.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// apiBaseURL is a var so tests can point the collector at an httptest server.
var apiBaseURL = "https://api.github.com"

// maxArtifactBytes caps the in-memory artifact download. Outputs are metadata,
// not payloads; anything larger is a misuse of the channel.
const maxArtifactBytes = 10 << 20

// maxOutputBytes is GitHub's 1 MB limit on a single step output.
const maxOutputBytes = 1_000_000

// maxPages bounds pagination so a looping Link header cannot hang the step.
const maxPages = 50

func main() {
	owner := flag.String("owner", "", "Target repository owner")
	repo := flag.String("repo", "", "Target repository name")
	runID := flag.String("run-id", "", "Run ID to collect outputs from")
	artifact := flag.String("artifact", "workflow-outputs", "Name of the artifact holding the outputs JSON")
	stepPrefix := flag.String("step-prefix", "outputs::", "Step-name prefix marking an inline outputs JSON payload")
	flag.Parse()

	if *owner == "" || *repo == "" || *runID == "" {
		fmt.Fprintf(os.Stderr, "Error: owner, repo, and run-id are required\n")
		os.Exit(1)
	}

	token := os.Getenv("GH_TOKEN")
	if token == "" {
		fmt.Fprintf(os.Stderr, "Error: GH_TOKEN environment variable not set\n")
		os.Exit(1)
	}

	fmt.Printf("INFO: Collect Outputs\n")
	fmt.Printf("INFO:   Target: %s/%s\n", *owner, *repo)
	fmt.Printf("INFO:   Run ID: %s\n", *runID)
	fmt.Printf("INFO:   Artifact: %s\n", *artifact)
	fmt.Printf("INFO:   Step prefix: %s\n", *stepPrefix)

	c := &collector{token: token, client: &http.Client{Timeout: 30 * time.Second}}

	outputs, source := c.collect(*owner, *repo, *runID, *artifact, *stepPrefix)

	fmt.Printf("INFO: Writing remote_outputs (source=%s) to GITHUB_OUTPUT\n", source)
	if err := appendOutput("remote_outputs", outputs); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}
	if err := appendOutput("remote_outputs_source", source); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	if source == "none" {
		fmt.Printf("WARN: No outputs found. The remote workflow returned no artifact named %q and no step named %q<json>.\n", *artifact, *stepPrefix)
		return
	}
	// The payload is not logged: it may hold values that are secret in the
	// remote repository but unmasked here.
	fmt.Printf("OK: Collected outputs from %s (%d bytes)\n", source, len(outputs))
}

type collector struct {
	token  string
	client *http.Client
}

// collect tries the artifact channel, then the step-name channel. A failure in
// either is logged and treated as "absent": the remote run already happened, so
// a collection problem must not mask its result.
func (c *collector) collect(owner, repo, runID, artifact, stepPrefix string) (outputs, source string) {
	if artifact != "" {
		payload, err := c.fromArtifact(owner, repo, runID, artifact)
		switch {
		case err != nil:
			fmt.Printf("WARN: Artifact lookup failed: %v\n", err)
		case payload != "":
			return payload, "artifact"
		}
	}

	if stepPrefix != "" {
		payload, err := c.fromStepName(owner, repo, runID, stepPrefix)
		switch {
		case err != nil:
			fmt.Printf("WARN: Step-name lookup failed: %v\n", err)
		case payload != "":
			return payload, "step"
		}
	}

	return "{}", "none"
}

// fromArtifact downloads the named artifact and returns the compacted JSON it
// contains. An empty return with a nil error means the artifact is absent.
func (c *collector) fromArtifact(owner, repo, runID, name string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%s/artifacts?per_page=100", apiBaseURL, owner, repo, runID)
	sawExpired := false
	for page := 0; url != ""; page++ {
		if page >= maxPages {
			return "", fmt.Errorf("artifacts listing exceeded %d pages", maxPages)
		}
		body, next, err := c.getPage(url)
		if err != nil {
			return "", err
		}

		var list struct {
			Artifacts []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Expired bool   `json:"expired"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return "", fmt.Errorf("parsing artifacts response: %w", err)
		}

		for _, a := range list.Artifacts {
			if a.Name != name {
				continue
			}
			if a.Expired {
				sawExpired = true
				continue
			}
			fmt.Printf("INFO: Downloading artifact %q (id %d)\n", name, a.ID)
			// The zip endpoint 302s to a signed storage URL; Go drops the
			// Authorization header across hosts, which that URL requires.
			zipBytes, err := c.get(fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d/zip", apiBaseURL, owner, repo, a.ID))
			if err != nil {
				return "", err
			}
			return jsonFromZip(zipBytes)
		}
		url = next
	}

	if sawExpired {
		return "", fmt.Errorf("artifact %q has expired", name)
	}
	fmt.Printf("INFO: No artifact named %q on this run\n", name)
	return "", nil
}

// jsonFromZip extracts the outputs JSON from an artifact archive, preferring
// outputs.json when the archive holds several JSON files.
func jsonFromZip(zipBytes []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", fmt.Errorf("reading artifact zip: %w", err)
	}

	var chosen *zip.File
	for _, f := range r.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}
		if strings.EqualFold(f.Name, "outputs.json") {
			chosen = f
			break
		}
		if chosen == nil {
			chosen = f
		}
	}
	if chosen == nil {
		return "", fmt.Errorf("artifact contains no .json file")
	}

	rc, err := chosen.Open()
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", chosen.Name, err)
	}
	defer rc.Close()

	content, err := io.ReadAll(io.LimitReader(rc, maxArtifactBytes))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", chosen.Name, err)
	}

	fmt.Printf("INFO: Using %s from artifact\n", chosen.Name)
	return compactJSON(content)
}

// fromStepName scans the run's steps for a name of the form "<prefix><json>".
// An empty return with a nil error means no step carried a payload.
func (c *collector) fromStepName(owner, repo, runID, prefix string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%s/jobs?per_page=100", apiBaseURL, owner, repo, runID)

	// Last match wins: the payload step is conventionally the final one, and a
	// re-run appends rather than replaces.
	found := ""
	for page := 0; url != ""; page++ {
		if page >= maxPages {
			return "", fmt.Errorf("jobs listing exceeded %d pages", maxPages)
		}
		body, next, err := c.getPage(url)
		if err != nil {
			return "", err
		}

		var list struct {
			Jobs []struct {
				Name  string `json:"name"`
				Steps []struct {
					Name string `json:"name"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return "", fmt.Errorf("parsing jobs response: %w", err)
		}

		for _, job := range list.Jobs {
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Name, prefix) {
					fmt.Printf("INFO: Found outputs on a step of job %q\n", job.Name)
					found = strings.TrimSpace(strings.TrimPrefix(step.Name, prefix))
				}
			}
		}
		url = next
	}
	if found == "" {
		return "", nil
	}
	return compactJSON([]byte(found))
}

// compactJSON validates the payload is a JSON object and flattens it to one
// line, which is what GITHUB_OUTPUT and downstream fromJSON() expect.
func compactJSON(raw []byte) (string, error) {
var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("outputs are not a JSON object: %w", err)
	}
	if obj == nil {
		return "", fmt.Errorf("outputs are JSON null, not an object")
	}
	compacted, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("re-encoding outputs: %w", err)
	}
	if len(compacted) > maxOutputBytes {
		return "", fmt.Errorf("outputs are %d bytes, over the %d-byte step output limit", len(compacted), maxOutputBytes)
	}
	return string(compacted), nil
}

func (c *collector) get(url string) ([]byte, error) {
	body, _, err := c.getPage(url)
	return body, err
}

// getPage fetches url and returns the rel="next" URL from its Link header, or
// "" on the last page.
func (c *collector) getPage(url string) ([]byte, string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes))
	if err != nil {
		return nil, "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("GET %s returned HTTP %d: %s", url, resp.StatusCode, truncate(string(body), 300))
	}
	next := nextLink(resp.Header.Get("Link"))
	// The token rides on every request, so never follow a link off the API host.
	if next != "" && !strings.HasPrefix(next, apiBaseURL+"/") {
		return nil, "", fmt.Errorf("refusing to follow pagination link outside %s", apiBaseURL)
	}
	return body, next, nil
}

// nextLink extracts the rel="next" target from a GitHub Link header.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		for _, attr := range segments[1:] {
			if strings.TrimSpace(attr) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(segments[0]), "<>")
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func appendOutput(key, value string) error {
	outputFile := os.Getenv("GITHUB_OUTPUT")
	if outputFile == "" {
		return fmt.Errorf("GITHUB_OUTPUT environment variable not set")
	}

	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(fmt.Sprintf("%s=%s\n", key, value))
	return err
}
