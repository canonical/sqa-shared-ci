package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// fakeAPI stands in for the GitHub REST endpoints the collector touches.
type fakeAPI struct {
	artifacts string // JSON body for the run artifacts listing
	zipBody   []byte // body for the artifact zip download
	jobs      string // JSON body for the run jobs listing
}

func (f *fakeAPI) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/actions/runs/1/artifacts", func(w http.ResponseWriter, _ *http.Request) {
		if f.artifacts == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, f.artifacts)
	})
	mux.HandleFunc("/repos/o/r/actions/artifacts/7/zip", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(f.zipBody)
	})
	mux.HandleFunc("/repos/o/r/actions/runs/1/jobs", func(w http.ResponseWriter, _ *http.Request) {
		if f.jobs == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, f.jobs)
	})

	srv := httptest.NewServer(mux)
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
		artifacts: `{"artifacts":[{"id":7,"name":"workflow-outputs","expired":false}]}`,
		zipBody:   zipWith(t, map[string]string{"outputs.json": `{"from":"artifact"}`}),
		jobs:      `{"jobs":[{"name":"j","steps":[{"name":"outputs::{\"from\":\"step\"}"}]}]}`,
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
		artifacts: `{"artifacts":[]}`,
		jobs:      `{"jobs":[{"name":"j","steps":[{"name":"Checkout"},{"name":"outputs::{\"artefact_id\":414047}"}]}]}`,
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
		artifacts: `{"artifacts":[]}`,
		jobs:      `{"jobs":[{"name":"j","steps":[{"name":"Checkout"}]}]}`,
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
		artifacts: `{"artifacts":[]}`,
		jobs:      `{"jobs":[{"name":"j","steps":[{"name":"outputs::not json"}]}]}`,
	}
	api.start(t)

	_, source := newCollector().collect("o", "r", "1", "workflow-outputs", "outputs::")

	if source != "none" {
		t.Errorf("expected malformed payload to yield source=none, got %q", source)
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
