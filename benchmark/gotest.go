package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunFullSuite runs `go test -json -count=1 ./...` in repoDir and parses the
// test2json stream into per-package results. A non-zero exit is data (test
// failures), not an error; an error is returned only when the stream yields
// no package events at all.
func RunFullSuite(ctx context.Context, repoDir string, extraArgs []string) (map[string]PackageResult, error) {
	args := append([]string{"test", "-json", "-count=1", "./..."}, extraArgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	_ = err // failures are data
	return ParseTestJSON(out)
}

type testEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

// ParseTestJSON parses a test2json stream.
func ParseTestJSON(data []byte) (map[string]PackageResult, error) {
	results := map[string]*PackageResult{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Package == "" {
			continue
		}
		r, ok := results[ev.Package]
		if !ok {
			r = &PackageResult{ImportPath: ev.Package, Passed: true}
			results[ev.Package] = r
		}
		if ev.Test == "" {
			switch ev.Action {
			case "pass":
				r.Passed = true
				r.Elapsed = time.Duration(ev.Elapsed * float64(time.Second))
			case "fail":
				r.Passed = false
				r.Elapsed = time.Duration(ev.Elapsed * float64(time.Second))
			case "skip":
				r.Skipped = true
			}
		} else if ev.Action == "fail" {
			r.Passed = false
			r.FailedTests = append(r.FailedTests, ev.Test)
		}
		if ev.Test == "" && strings.Contains(ev.Output, "[no test files]") {
			r.Skipped = true
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("go test: no package events in output")
	}
	out := map[string]PackageResult{}
	for k, v := range results {
		out[k] = *v
	}
	return out, nil
}
