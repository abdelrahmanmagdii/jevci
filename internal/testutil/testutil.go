// Package testutil builds a temp git repo from the fixture module.
package testutil

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// FixtureRepo copies internal/testdata/fixture into a temp dir, creates a
// git repo there and commits it as "base". It returns the repo dir and a
// helper that commits all current changes with a message and returns the
// new HEAD sha.
func FixtureRepo(t *testing.T) (dir string, commit func(msg string) string) {
	t.Helper()
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	src := fixtureDir(t)
	dst := t.TempDir()
	copyTree(t, src, dst)
	run(t, dst, "git", "init", "-q")
	run(t, dst, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "add", "-A")
	commit = func(msg string) string {
		run(t, dst, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "add", "-A")
		run(t, dst, "git", "-c", "user.name=test", "-c", "user.email=test@example.com",
			"commit", "-q", "-m", msg, "--allow-empty")
		out := run(t, dst, "git", "rev-parse", "HEAD")
		return string(out[:len(out)-1])
	}
	commit("base")
	return dst, commit
}

// WriteFile writes a file inside dir, creating parents.
func WriteFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	// testutil lives at internal/testutil; fixture at internal/testdata/fixture.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := wd; ; d = filepath.Dir(d) {
		cand := filepath.Join(d, "internal", "testdata", "fixture")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
		if filepath.Dir(d) == d {
			t.Fatal("fixture dir not found")
		}
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return out
}
