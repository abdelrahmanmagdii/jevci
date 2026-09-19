package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCalibrationCLI(t *testing.T) {
	if got := run([]string{"calibrate"}); got != 2 {
		t.Fatalf("missing arguments: %d", got)
	}
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite.yaml")
	oracle := filepath.Join(dir, "oracle.jsonl")
	out := filepath.Join(dir, "scores.jsonl")
	if err := os.WriteFile(suite, []byte("repo: fixture\nworkdir: unused\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oracle, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"calibrate", "--suite", suite, "--oracle", oracle, "--out", out, "--config", filepath.Join(dir, "absent.yaml")}
	t.Setenv("TYPESAFE_API_KEY", "")
	if got := run(args); got != 1 {
		t.Fatalf("missing key: %d", got)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("missing key created output: %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "offline-test")
	if err := os.WriteFile(out, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run(args); got != 1 {
		t.Fatalf("existing output: %d", got)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "keep" {
		t.Fatalf("output overwritten: %q, %v", data, err)
	}
}
