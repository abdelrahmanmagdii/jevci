// Package config loads and validates .jevci.yaml.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CandidatesMode selects which packages Jev scores.
type CandidatesMode string

const (
	CandidatesTransitiveDependents CandidatesMode = "transitive_dependents"
	CandidatesAllUnselected        CandidatesMode = "all_unselected"
)

// UnattributedMode selects what to do with changed files that map to no
// Go package.
type UnattributedMode string

const (
	UnattributedRunAll UnattributedMode = "run_all"
	UnattributedIgnore UnattributedMode = "ignore"
)

// NestedModulesMode selects what to do with changed files inside nested
// Go modules.
type NestedModulesMode string

const (
	NestedModulesIgnore NestedModulesMode = "ignore"
	NestedModulesRunAll NestedModulesMode = "run_all"
)

// Jev configures the Jev scoring backend.
type Jev struct {
	Enabled            bool           `yaml:"enabled"`
	Model              string         `yaml:"model"`
	BaseURL            string         `yaml:"base_url"`
	APIKeyEnv          string         `yaml:"api_key_env"`
	RunThreshold       float64        `yaml:"run_threshold"`
	UncertainThreshold float64        `yaml:"uncertain_threshold"`
	BatchSize          int            `yaml:"batch_size"`
	Concurrency        int            `yaml:"concurrency"`
	Timeout            time.Duration  `yaml:"timeout"`
	MaxRetries         int            `yaml:"max_retries"`
	Candidates         CandidatesMode `yaml:"candidates"`
	MaxDiffChars       int            `yaml:"max_diff_chars"`
	MaxStateChars      int            `yaml:"max_state_chars"`
	DiffContextLines   int            `yaml:"diff_context_lines"`
}

// Safety configures the policy's deterministic rules.
type Safety struct {
	AlwaysRunChangedPackages  bool              `yaml:"always_run_changed_packages"`
	AlwaysRunChangedTests     bool              `yaml:"always_run_changed_tests"`
	AlwaysRunDirectDependents bool              `yaml:"always_run_direct_dependents"`
	FailOpen                  bool              `yaml:"fail_open"`
	RunAllOnModuleChange      bool              `yaml:"run_all_on_module_change"`
	UnattributedFiles         UnattributedMode  `yaml:"unattributed_files"`
	NestedModules             NestedModulesMode `yaml:"nested_modules"`
	ProtectedTests            []string          `yaml:"protected_tests"`
	IgnoreFiles               []string          `yaml:"ignore_files"`
}

// Config is the parsed .jevci.yaml.
type Config struct {
	Jev    Jev    `yaml:"jev"`
	Safety Safety `yaml:"safety"`
}

// Default returns the documented defaults.
func Default() Config {
	return Config{
		Jev: Jev{
			Enabled:            true,
			Model:              "jev-latest",
			BaseURL:            "https://api.typesafe.ai",
			APIKeyEnv:          "TYPESAFE_API_KEY",
			RunThreshold:       0.70,
			UncertainThreshold: 0.30,
			BatchSize:          8,
			Concurrency:        4,
			Timeout:            30 * time.Second,
			MaxRetries:         3,
			Candidates:         CandidatesTransitiveDependents,
			MaxDiffChars:       24000,
			MaxStateChars:      90000,
			DiffContextLines:   3,
		},
		Safety: Safety{
			AlwaysRunChangedPackages:  true,
			AlwaysRunChangedTests:     true,
			AlwaysRunDirectDependents: true,
			FailOpen:                  true,
			RunAllOnModuleChange:      true,
			UnattributedFiles:         UnattributedRunAll,
			NestedModules:             NestedModulesIgnore,
			IgnoreFiles: []string{
				"**/*.md", "docs/**", ".github/**", ".gitlab-ci.yml",
				".circleci/**", ".travis.yml", "LICENSE*", "NOTICE*",
				"CHANGELOG*", "CODEOWNERS", ".gitignore", ".gitattributes",
				".editorconfig", ".golangci.yml", ".golangci.yaml",
				".goreleaser.yml", ".goreleaser.yaml",
				".pre-commit-config.yaml", "Makefile", "Makefile.*",
				"VERSION", ".jevci.yaml",
			},
		},
	}
}

// Load reads path; a missing file yields defaults without error. Fields not
// present in the file keep their defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks invariants.
func (c Config) Validate() error {
	if c.Jev.UncertainThreshold < 0 || c.Jev.UncertainThreshold > 1 {
		return fmt.Errorf("jev.uncertain_threshold %v out of [0,1]", c.Jev.UncertainThreshold)
	}
	if c.Jev.RunThreshold < 0 || c.Jev.RunThreshold > 1 {
		return fmt.Errorf("jev.run_threshold %v out of [0,1]", c.Jev.RunThreshold)
	}
	if c.Jev.UncertainThreshold > c.Jev.RunThreshold {
		return fmt.Errorf("jev.uncertain_threshold %v > jev.run_threshold %v", c.Jev.UncertainThreshold, c.Jev.RunThreshold)
	}
	if c.Jev.BatchSize < 1 {
		return fmt.Errorf("jev.batch_size %d must be >= 1", c.Jev.BatchSize)
	}
	switch c.Jev.Candidates {
	case CandidatesTransitiveDependents, CandidatesAllUnselected:
	default:
		return fmt.Errorf("jev.candidates %q must be %q or %q", c.Jev.Candidates, CandidatesTransitiveDependents, CandidatesAllUnselected)
	}
	switch c.Safety.UnattributedFiles {
	case UnattributedRunAll, UnattributedIgnore:
	default:
		return fmt.Errorf("safety.unattributed_files %q must be %q or %q", c.Safety.UnattributedFiles, UnattributedRunAll, UnattributedIgnore)
	}
	switch c.Safety.NestedModules {
	case NestedModulesIgnore, NestedModulesRunAll:
	default:
		return fmt.Errorf("safety.nested_modules %q must be %q or %q", c.Safety.NestedModules, NestedModulesIgnore, NestedModulesRunAll)
	}
	return nil
}

// MatchGlob matches a doublestar glob (relative to repo root) against path.
// `**` matches any number of segments, `*` within a segment, `?` one char.
func MatchGlob(pattern, path string) bool {
	pat := strings.Split(pattern, "/")
	parts := strings.Split(path, "/")
	return matchParts(pat, parts)
}

func matchParts(pat, parts []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(parts); i++ {
				if matchParts(pat[1:], parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 || !matchSegment(pat[0], parts[0]) {
			return false
		}
		pat = pat[1:]
		parts = parts[1:]
	}
	return len(parts) == 0
}

func matchSegment(pat, s string) bool {
	// classic * ? matcher
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		if px < len(pat) && (pat[px] == '?' || pat[px] == s[sx]) {
			px++
			sx++
		} else if px < len(pat) && pat[px] == '*' {
			star = px
			mark = sx
			px++
		} else if star != -1 {
			px = star + 1
			mark++
			sx = mark
		} else {
			return false
		}
	}
	for px < len(pat) && pat[px] == '*' {
		px++
	}
	return px == len(pat)
}

// MatchPackagePattern reports whether display path p matches pattern:
// "./a/b/..." matches the prefix, "./a/b" matches exactly.
func MatchPackagePattern(pattern, p string) bool {
	if strings.HasSuffix(pattern, "/...") {
		prefix := strings.TrimSuffix(pattern, "/...")
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	return pattern == p
}
