package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Jev.Enabled || c.Jev.Model != "jev-latest" {
		t.Fatalf("unexpected defaults: %+v", c.Jev)
	}
}

func TestLoadPartial(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".jevci.yaml")
	if err := os.WriteFile(p, []byte("jev:\n  run_threshold: 0.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Jev.RunThreshold != 0.5 || c.Jev.BatchSize != 8 {
		t.Fatalf("%+v", c.Jev)
	}
}

func TestValidate(t *testing.T) {
	ok := func(m func(*Config)) Config {
		c := Default()
		m(&c)
		return c
	}
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"default", Default(), true},
		{"negative run", ok(func(c *Config) { c.Jev.RunThreshold = -0.1 }), false},
		{"run > 1", ok(func(c *Config) { c.Jev.RunThreshold = 1.1 }), false},
		{"uncertain > run", ok(func(c *Config) { c.Jev.UncertainThreshold = 0.9 }), false},
		{"equal thresholds", ok(func(c *Config) { c.Jev.RunThreshold = 0.5; c.Jev.UncertainThreshold = 0.5 }), true},
		{"zero batch", ok(func(c *Config) { c.Jev.BatchSize = 0 }), false},
		{"bad candidates", ok(func(c *Config) { c.Jev.Candidates = "nope" }), false},
		{"all_unselected", ok(func(c *Config) { c.Jev.Candidates = CandidatesAllUnselected }), true},
		{"bad unattributed", ok(func(c *Config) { c.Safety.UnattributedFiles = "x" }), false},
		{"nested run_all", ok(func(c *Config) { c.Safety.NestedModules = NestedModulesRunAll }), true},
		{"bad nested", ok(func(c *Config) { c.Safety.NestedModules = "x" }), false},
	}
	for _, tc := range cases {
		err := tc.cfg.Validate()
		if (err == nil) != tc.want {
			t.Errorf("%s: err=%v", tc.name, err)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/a/b.md", true},
		{"**/*.md", "x.go", false},
		{"docs/**", "docs/a/b.txt", true},
		{"docs/**", "docsx/a", false},
		{".github/**", ".github/workflows/ci.yml", true},
		{"LICENSE", "LICENSE", true},
		{"LICENSE", "sub/LICENSE", false},
		{"*.go", "main.go", true},
		{"*.go", "pkg/main.go", false},
		{"pkg/*.go", "pkg/a.go", true},
		{"pkg/*_test.go", "pkg/x_test.go", true},
		{"a/**/c.txt", "a/c.txt", true},
		{"a/**/c.txt", "a/b/b/c.txt", true},
		{"a?c", "abc", true},
		{"Makefile", "Makefile", true},
		{"Makefile", "sub/Makefile", false},
		{"Makefile.*", "Makefile.common", true},
		{"Makefile.*", "sub/Makefile.common", false},
		{"**/Makefile", "sub/dir/Makefile", true},
		{"LICENSE*", "LICENSE.md", true},
		{".golangci.yml", ".golangci.yml", true},
		{".golangci.yml", "ci/.golangci.yml", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.path); got != c.want {
			t.Errorf("MatchGlob(%q,%q)=%v want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestMatchPackagePattern(t *testing.T) {
	if !MatchPackagePattern("./test/e2e/...", "./test/e2e/foo") {
		t.Error("prefix match")
	}
	if !MatchPackagePattern("./test/e2e/...", "./test/e2e") {
		t.Error("exact under prefix")
	}
	if MatchPackagePattern("./test/e2e/...", "./test/e2eX") {
		t.Error("prefix boundary")
	}
	if !MatchPackagePattern("./pkg/foo", "./pkg/foo") {
		t.Error("exact")
	}
	if MatchPackagePattern("./pkg/foo", "./pkg/foo/bar") {
		t.Error("exact should not match child")
	}
}
