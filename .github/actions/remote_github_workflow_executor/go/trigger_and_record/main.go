package main

import (
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

func main() {
	owner := flag.String("owner", "", "Target repository owner")
	repo := flag.String("repo", "", "Target repository name")
	workflow := flag.String("workflow", "", "Workflow file or ID")
	ref := flag.String("ref", "main", "Target git ref")
	inputsJSON := flag.String("inputs", "{}", "JSON object string for workflow inputs")
	flag.Parse()

	if *owner == "" || *repo == "" || *workflow == "" {
		fmt.Fprintf(os.Stderr, "Error: owner, repo, and workflow are required\n")
		os.Exit(1)
	}

	token := os.Getenv("GH_TOKEN")
	if token == "" {
		fmt.Fprintf(os.Stderr, "Error: GH_TOKEN environment variable not set\n")
		os.Exit(1)
	}

	fmt.Printf("INFO: Trigger and Record\n")
	fmt.Printf("INFO:   Target: %s/%s\n", *owner, *repo)
	fmt.Printf("INFO:   Workflow: %s\n", *workflow)
	fmt.Printf("INFO:   Ref: %s\n", *ref)

	dispatchedAt := time.Now().UTC()
	dispatchedID, err := dispatchWorkflow(token, *owner, *repo, *workflow, *ref, *inputsJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error dispatching workflow: %v\n", err)
		os.Exit(1)
	}

	var runID string
	if dispatchedID > 0 {
		runID = fmt.Sprintf("%d", dispatchedID)
	} else {
		// Only reached when the API ignored return_run_details (e.g. older GHES).
		fmt.Printf("WARN: Dispatch response carried no run ID; falling back to run lookup\n")
		fmt.Printf("INFO: Waiting 5s for run to be registered...\n")
		time.Sleep(5 * time.Second)

		runID, err = findRunID(token, *owner, *repo, *workflow, dispatchedAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding run ID: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("INFO: Writing run_id=%s to GITHUB_OUTPUT\n", runID)
	// Write to GITHUB_OUTPUT
	if err := appendOutput("run_id", runID); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("OK: Found workflow run: %s\n", runID)
}

// dispatchWorkflow returns the created run's ID, or 0 when the API did not report it.
func dispatchWorkflow(token, owner, repo, workflow, ref, inputsJSON string) (int64, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows/%s/dispatches",
		owner, repo, workflow)

	// Parse inputs JSON
	var inputs map[string]interface{}
	if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
		return 0, fmt.Errorf("invalid inputs JSON: %w", err)
	}

	return doDispatch(token, url, ref, inputs)
}

func doDispatch(token, url, ref string, inputs map[string]interface{}) (int64, error) {
	body := map[string]interface{}{
		"ref":                ref,
		"inputs":             inputs,
		"return_run_details": true,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	for attempt := 1; attempt <= 5; attempt++ {
		fmt.Printf("INFO: [Dispatch attempt %d/5] POST %s\n", attempt, url)

		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return 0, err
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			if attempt < 5 {
				delay := time.Duration(1<<uint(attempt-1)) * 2 * time.Second
				fmt.Printf("RETRY: Transient dispatch error, retrying in %v\n", delay)
				time.Sleep(delay)
				continue
			}
			return 0, err
		}

		if resp.StatusCode == 204 {
			resp.Body.Close()
			fmt.Printf("INFO: Dispatch successful (HTTP 204)\n")
			fmt.Println("OK: Workflow dispatch triggered successfully")
			return 0, nil
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			var details struct {
				WorkflowRunID int64 `json:"workflow_run_id"`
			}
			if err := json.Unmarshal(respBody, &details); err != nil {
				fmt.Printf("WARN: Could not parse dispatch response: %v\n", err)
			}
			fmt.Printf("INFO: Dispatch successful (HTTP 200, run %d)\n", details.WorkflowRunID)
			fmt.Println("OK: Workflow dispatch triggered successfully")
			return details.WorkflowRunID, nil
		}

		// If 422 due to unexpected "metadata" input, strip it and retry without
		if resp.StatusCode == 422 && inputs["metadata"] != nil {
			if isUnexpectedInputsError(respBody) {
				fmt.Printf("WARN: Target workflow does not accept 'metadata' input — retrying without it\n")
				delete(inputs, "metadata")
				return doDispatch(token, url, ref, inputs)
			}
		}

		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
			return 0, fmt.Errorf("non-retriable dispatch error (HTTP %d): %s", resp.StatusCode, string(respBody))
		}

		if attempt < 5 {
			delay := time.Duration(1<<uint(attempt-1)) * 2 * time.Second
			fmt.Printf("RETRY: Transient dispatch error (HTTP %d), retrying in %v\n", resp.StatusCode, delay)
			time.Sleep(delay)
			continue
		}

		return 0, fmt.Errorf("dispatch failed after 5 attempts: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return 0, nil
}

// isUnexpectedInputsError checks if a 422 response is about unexpected workflow inputs.
func isUnexpectedInputsError(body []byte) bool {
	var errResp struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) != nil {
		return false
	}
	return strings.Contains(errResp.Message, "Unexpected inputs provided")
}

// findRunID guesses the run created by our dispatch: the earliest run created
// after dispatchedAt. Concurrent dispatches can still make this ambiguous.
func findRunID(token, owner, repo, workflow string, dispatchedAt time.Time) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/workflows/%s/runs?event=workflow_dispatch&per_page=20&created=%%3E%%3D%s",
		owner, repo, workflow, dispatchedAt.Add(-10*time.Second).Format(time.RFC3339))

	for attempt := 1; attempt <= 8; attempt++ {
		fmt.Printf("INFO: [Find attempt %d/8] Fetching recent workflow runs...\n", attempt)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}

		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("Accept", "application/vnd.github+json")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			if attempt < 8 {
				delay := time.Duration(1<<uint(attempt-1)) * 3 * time.Second
				fmt.Printf("RETRY: Transient lookup error, retrying in %v\n", delay)
				time.Sleep(delay)
				continue
			}
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)

			if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
				return "", fmt.Errorf("non-retriable lookup error (HTTP %d): %s", resp.StatusCode, string(respBody))
			}

			if attempt < 8 {
				delay := time.Duration(1<<uint(attempt-1)) * 3 * time.Second
				fmt.Printf("RETRY: Transient lookup error (HTTP %d), retrying in %v\n", resp.StatusCode, delay)
				time.Sleep(delay)
				continue
			}

			return "", fmt.Errorf("lookup failed after 8 attempts: HTTP %d: %s", resp.StatusCode, string(respBody))
		}

		var result struct {
			WorkflowRuns []struct {
				ID int64 `json:"id"`
			} `json:"workflow_runs"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", err
		}

		if n := len(result.WorkflowRuns); n > 0 {
			if n > 1 {
				fmt.Printf("WARN: %d runs created since dispatch; picking the earliest, which may not be ours\n", n)
			}
			// Runs are listed newest first.
			return fmt.Sprintf("%d", result.WorkflowRuns[n-1].ID), nil
		}

		if attempt < 8 {
			delay := time.Duration(1<<uint(attempt-1)) * 3 * time.Second
			fmt.Printf("RETRY: Run not yet registered, retrying in %v\n", delay)
			time.Sleep(delay)
			continue
		}

		return "", fmt.Errorf("could not find workflow run after 8 attempts")
	}

	return "", fmt.Errorf("failed to find run after 8 attempts")
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
