package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunFullSuite runs `go test -json -count=1` over the explicit package list
// pkgs in repoDir and parses the test2json stream into per-package results.
// A complete package failure is data. Interrupted or incomplete runs,
// malformed output, and missing requested package results are errors.
func RunFullSuite(ctx context.Context, repoDir string, pkgs []string, timeout time.Duration, extraArgs []string) (map[string]PackageResult, error) {
	return runFullSuite(ctx, repoDir, pkgs, timeout, extraArgs, "")
}

func runFullSuite(ctx context.Context, repoDir string, pkgs []string, timeout time.Duration, extraArgs []string, logDir string) (map[string]PackageResult, error) {
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("go test: empty package universe")
	}
	args := []string{"test", "-json", "-count=1"}
	if timeout > 0 {
		args = append(args, "-timeout", timeout.String())
	}
	args = append(args, pkgs...)
	args = append(args, extraArgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return nil, err
		}
		for name, dst := range map[string]*io.Writer{"stdout.jsonl": &cmd.Stdout, "stderr.log": &cmd.Stderr} {
			f, err := os.OpenFile(filepath.Join(logDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			*dst = io.MultiWriter(*dst, f)
		}
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("go test interrupted: %w", ctx.Err())
	}
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return nil, fmt.Errorf("go test process: %w", err)
	}
	_ = err // failures are data
	results, parseErr := ParseTestJSON(stdout.Bytes())
	if parseErr != nil {
		return nil, fmt.Errorf("%w; stderr: %s", parseErr, strings.TrimSpace(stderr.String()))
	}
	failed := false
	for _, ip := range pkgs {
		r, ok := results[ip]
		if !ok {
			return nil, fmt.Errorf("go test: missing result for requested package %s", ip)
		}
		failed = failed || (!r.Passed && !r.Skipped)
	}
	if err != nil && !failed {
		return nil, fmt.Errorf("go test exited unsuccessfully without a package failure: %w", err)
	}
	return results, nil
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
	completed := map[string]bool{}
	ranTests := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("go test: malformed JSON event: %w", err)
		}
		if ev.Package == "" {
			continue
		}
		r, ok := results[ev.Package]
		if !ok {
			r = &PackageResult{ImportPath: ev.Package}
			results[ev.Package] = r
		}
		if ev.Test != "" && ev.Action == "run" {
			ranTests[ev.Package] = true
		}
		if ev.Test == "" {
			switch ev.Action {
			case "pass", "fail", "skip":
				if completed[ev.Package] {
					return nil, fmt.Errorf("go test: duplicate terminal event for %s", ev.Package)
				}
				completed[ev.Package] = true
				r.Passed = ev.Action == "pass"
				r.Skipped = ev.Action == "skip" || r.Skipped
				r.Elapsed = time.Duration(ev.Elapsed * float64(time.Second))
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
		if !completed[k] {
			return nil, fmt.Errorf("go test: incomplete result for %s", k)
		}
		if v.Passed && !ranTests[k] {
			v.Skipped = true
		}
		out[k] = *v
	}
	return out, nil
}
