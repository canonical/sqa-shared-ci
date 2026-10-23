package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// zipWith builds an artifact archive containing the given files.
func zipWith(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("creating zip entry %s: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("writing zip entry %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

// fakeAPI stands in for the GitHub REST endpoints the collector touches. The
// listings are paged: each element is one page, linked via rel="next".
type fakeAPI struct {
	artifacts []string // JSON bodies for the run artifacts listing
	zipBody   []byte   // body for the artifact zip download
	jobs      []string // JSON bodies for the run jobs listing
}

func servePages(pages []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(pages) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		if page < len(pages) {
			next := fmt.Sprintf("http://%s%s?per_page=100&page=%d", r.Host, r.URL.Path, page+1)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, next, next))
		}
		fmt.Fprint(w, pages[page-1])
	}
}

func (f *fakeAPI) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/actions/runs/1/artifacts", servePages(f.artifacts))
	mux.HandleFunc("/repos/o/r/actions/artifacts/7/zip", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(f.zipBody)
	})
	mux.HandleFunc("/repos/o/r/actions/runs/1/jobs", servePages(f.jobs))

	useServer(t, mux)
}

// useServer points the collector at handler for the duration of the test.
func useServer(t *testing.T, handler http.Handler) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	original := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = original })
}

func newCollector() *collector {
	return &collector{token: "t", client: http.DefaultClient}
}

// TestCollect_PrefersArtefact pins the channel precedence: when both an
// artifact and a marked step are present, the artifact wins.
func TestCollect_PrefersArtifact(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[{"id":7,"name":"workflow-outputs","expired":false}]}`},
		zipBody:   zipWith(t, map[string]string{"outputs.json": `{"from":"artifact"}`}),
		jobs:      []string{`{"jobs":[{"name":"j","steps":[{"name":"outputs::{\"from\":\"step\"}"}]}]}`},
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "artifact" {
		t.Errorf("expected source=artifact, got %q", source)
	}
	if outputs != `{"from":"artifact"}` {
		t.Errorf("unexpected outputs: %s", outputs)
	}
}

// TestCollect_FallsBackToStepName covers remote workflows that return data
// without uploading an artifact.
func TestCollect_FallsBackToStepName(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[]}`},
		jobs:      []string{`{"jobs":[{"name":"j","steps":[{"name":"Checkout"},{"name":"outputs::{\"artefact_id\":414047}"}]}]}`},
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "step" {
		t.Errorf("expected source=step, got %q", source)
	}
	if outputs != `{"artefact_id":414047}` {
		t.Errorf("unexpected outputs: %s", outputs)
	}
}

// TestCollect_NoOutputs pins that a remote workflow returning nothing is a
// normal, non-failing outcome.
func TestCollect_NoOutputs(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[]}`},
		jobs:      []string{`{"jobs":[{"name":"j","steps":[{"name":"Checkout"}]}]}`},
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "none" {
		t.Errorf("expected source=none, got %q", source)
	}
	if outputs != "{}" {
		t.Errorf("expected empty object, got %s", outputs)
	}
}

// TestCollect_APIErrorsDoNotMaskTheRun pins that a collection failure degrades
// to "none" rather than propagating: the remote run already completed.
func TestCollect_APIErrorsDoNotMaskTheRun(t *testing.T) {
	api := &fakeAPI{} // both endpoints 404
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "none" || outputs != "{}" {
		t.Errorf("expected empty outputs from source=none, got %s from %q", outputs, source)
	}
}

// TestCollect_MalformedPayloadIsRejected pins that a step name carrying
// non-object JSON is not passed through to callers as if it were valid.
func TestCollect_MalformedPayloadIsRejected(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[]}`},
		jobs:      []string{`{"jobs":[{"name":"j","steps":[{"name":"outputs::not json"}]}]}`},
	}
	api.start(t)

	_, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "none" {
		t.Errorf("expected malformed payload to yield source=none, got %q", source)
	}
}

// TestCollect_NullPayloadIsRejected pins that JSON null, which unmarshals into
// a map without error, is not passed through as a valid object.
func TestCollect_NullPayloadIsRejected(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[{"id":7,"name":"workflow-outputs","expired":false}]}`},
		zipBody:   zipWith(t, map[string]string{"outputs.json": `null`}),
		jobs:      []string{`{"jobs":[{"name":"j","steps":[{"name":"outputs::null"}]}]}`},
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "none" || outputs != "{}" {
		t.Errorf("expected null payloads to yield {} from source=none, got %s from %q", outputs, source)
	}
}

// TestCollect_FollowsArtifactPagination covers runs with more than one page of
// artifacts, where the outputs artifact is not on the first page.
func TestCollect_FollowsArtifactPagination(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{
			`{"artifacts":[{"id":1,"name":"logs","expired":false}]}`,
			`{"artifacts":[{"id":7,"name":"workflow-outputs","expired":false}]}`,
		},
		zipBody: zipWith(t, map[string]string{"outputs.json": `{"page":2}`}),
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "")

	if source != "artifact" || outputs != `{"page":2}` {
		t.Errorf("expected artifact from page 2, got %s from %q", outputs, source)
	}
}

// TestCollect_FollowsJobsPagination covers runs with more than one page of
// jobs, where the marked step is not on the first page.
func TestCollect_FollowsJobsPagination(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[]}`},
		jobs: []string{
			`{"jobs":[{"name":"a","steps":[{"name":"Checkout"}]}]}`,
			`{"jobs":[{"name":"b","steps":[{"name":"outputs::{\"page\":2}"}]}]}`,
		},
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "step" || outputs != `{"page":2}` {
		t.Errorf("expected step payload from page 2, got %s from %q", outputs, source)
	}
}

// TestCollect_SkipsExpiredArtifact pins that an expired artifact does not hide
// a live one with the same name further down the listing.
func TestCollect_SkipsExpiredArtifact(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{
			`{"artifacts":[{"id":3,"name":"workflow-outputs","expired":true}]}`,
			`{"artifacts":[{"id":7,"name":"workflow-outputs","expired":false}]}`,
		},
		zipBody: zipWith(t, map[string]string{"outputs.json": `{"live":true}`}),
	}
	api.start(t)

	outputs, source := newCollector().collect("o", "r", "1", "workflow-outputs", "")

	if source != "artifact" || outputs != `{"live":true}` {
		t.Errorf("expected the live artifact, got %s from %q", outputs, source)
	}
}

// TestFromArtifact_OnlyExpired pins that a run whose only match has expired
// reports that, rather than claiming the artifact is absent.
func TestFromArtifact_OnlyExpired(t *testing.T) {
	api := &fakeAPI{
		artifacts: []string{`{"artifacts":[{"id":3,"name":"workflow-outputs","expired":true}]}`},
	}
	api.start(t)

	if _, err := newCollector().fromArtifact("o", "r", "1", "workflow-outputs"); err == nil {
		t.Error("expected an error for an expired artifact")
	}
}

// TestGetPage_RefusesOffHostLink pins that the token is never sent to a
// pagination link pointing away from the API host.
func TestGetPage_RefusesOffHostLink(t *testing.T) {
	useServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://evil.example/steal?page=2>; rel="next"`)
		fmt.Fprint(w, `{"artifacts":[]}`)
	}))

	if _, err := newCollector().fromArtifact("o", "r", "1", "workflow-outputs"); err == nil {
		t.Error("expected an off-host pagination link to be refused")
	}
}

// TestFromStepName_StopsAtPageCap pins that a self-referencing Link header
// cannot loop forever.
func TestFromStepName_StopsAtPageCap(t *testing.T) {
	requests := 0
	useServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=1>; rel="next"`, r.Host, r.URL.Path))
		fmt.Fprint(w, `{"jobs":[]}`)
	}))

	if _, err := newCollector().fromStepName("o", "r", "1", "outputs::"); err == nil {
		t.Error("expected an error once the page cap is reached")
	}
	if requests != maxPages {
		t.Errorf("expected %d requests, got %d", maxPages, requests)
	}
}

// TestJSONFromZip_PrefersOutputsJSON pins the file choice when an artifact
// bundles more than one JSON file.
func TestJSONFromZip_PrefersOutputsJSON(t *testing.T) {
	zipBytes := zipWith(t, map[string]string{
		"aaa-other.json": `{"wrong":true}`,
		"outputs.json":   `{"right":true}`,
	})

	got, err := jsonFromZip(zipBytes)
	if err != nil {
		t.Fatalf("jsonFromZip: %v", err)
	}
	if got != `{"right":true}` {
		t.Errorf("expected outputs.json to win, got %s", got)
	}
}

func TestJSONFromZip_NoJSONFile(t *testing.T) {
	zipBytes := zipWith(t, map[string]string{"notes.txt": "hello"})

	if _, err := jsonFromZip(zipBytes); err == nil {
		t.Error("expected an error when the artifact holds no .json file")
	}
}

// TestCompactJSON_RejectsOversizedPayload pins that a payload over the step
// output limit degrades to an error instead of failing the step later.
func TestCompactJSON_RejectsOversizedPayload(t *testing.T) {
	big := `{"blob":"` + strings.Repeat("x", maxOutputBytes) + `"}`
	if _, err := compactJSON([]byte(big)); err == nil {
		t.Error("expected an error for a payload over the output limit")
	}
}

// TestCompactJSON pins that outputs are flattened to the single line
// GITHUB_OUTPUT requires.
func TestCompactJSON(t *testing.T) {
	got, err := compactJSON([]byte("{\n  \"a\": 1,\n  \"b\": \"two\"\n}"))
	if err != nil {
		t.Fatalf("compactJSON: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if bytes.ContainsAny([]byte(got), "\n\r") {
		t.Errorf("compacted output still contains newlines: %q", got)
	}
}
