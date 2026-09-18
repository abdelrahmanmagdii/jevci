package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

// PRSpec identifies one pull request to evaluate.
type PRSpec struct {
	Number int    `yaml:"number"`
	Base   string `yaml:"base,omitempty"` // resolved via merge-base when empty
	Head   string `yaml:"head,omitempty"` // resolved via FetchPRHead when empty
	Title  string `yaml:"title,omitempty"`
}

// Suite describes a benchmark run.
type Suite struct {
	Repo                string        `yaml:"repo"`    // github.com/owner/name or URL
	Workdir             string        `yaml:"workdir"` // clone destination
	PRs                 []PRSpec      `yaml:"prs"`
	Strategies          []string      `yaml:"strategies,omitempty"` // default full,changed,static,jevci
	RunTests            bool          `yaml:"run_tests"`
	Packages            []string      `yaml:"packages,omitempty"`              // go package patterns; default ["./..."]
	ExcludePackageRegex string        `yaml:"exclude_package_regex,omitempty"` // applied to `go list` output
	Config              string        `yaml:"config,omitempty"`                // .jevci.yaml, relative to the suite file
	Oracle              string        `yaml:"oracle,omitempty"`                // head | revert (default head)
	TestTimeout         time.Duration `yaml:"test_timeout,omitempty"`          // per go test invocation
	TestArgs            []string      `yaml:"test_args,omitempty"`
}

// LoadSuite reads a suite YAML file. Config is resolved relative to the
// suite file's directory.
func LoadSuite(path string) (Suite, error) {
	var s Suite
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(s.Strategies) == 0 {
		s.Strategies = []string{"full", "changed", "static", "jevci"}
	}
	if len(s.Packages) == 0 {
		s.Packages = []string{"./..."}
	}
	if s.Oracle == "" {
		s.Oracle = "head"
	}
	if s.Oracle != "head" && s.Oracle != "revert" {
		return s, fmt.Errorf("oracle %q must be head or revert", s.Oracle)
	}
	if s.TestTimeout == 0 {
		s.TestTimeout = 30 * time.Minute
	}
	if s.Config != "" && !filepath.IsAbs(s.Config) {
		s.Config = filepath.Join(filepath.Dir(path), s.Config)
	}
	return s, nil
}

// ScorerFactory returns a fresh scorer per strategy run (isolated usage
// accounting). It may return nil.
type ScorerFactory func() semantic.Scorer

type strategySpec struct {
	label    string
	st       policy.Strategy
	noDirect bool // jevci:no-direct
}

func parseStrategies(in []string) ([]strategySpec, error) {
	var out []strategySpec
	for _, s := range in {
		spec := strategySpec{label: s}
		name := s
		if s == "jevci:no-direct" {
			spec.st = policy.StrategyJevCI
			spec.noDirect = true
			out = append(out, spec)
			continue
		}
		st, err := policy.ParseStrategy(name)
		if err != nil {
			return nil, err
		}
		spec.st = st
		out = append(out, spec)
	}
	return out, nil
}

// Run executes the suite. Per PR it checks out head, resolves the package
// universe, plans every strategy, runs the head suite, optionally runs the
// revert oracle, and writes one JSONL record per PR as it completes.
// Progress is logged to stderr via log.Printf.
func Run(ctx context.Context, suite Suite, cfg config.Config, scorerFactory ScorerFactory, out io.Writer) ([]Record, error) {
	if err := EnsureClone(ctx, suite.Repo, suite.Workdir); err != nil {
		return nil, fmt.Errorf("clone %s: %w", suite.Repo, err)
	}
	strategies, err := parseStrategies(suite.Strategies)
	if err != nil {
		return nil, err
	}
	var exclude *regexp.Regexp
	if suite.ExcludePackageRegex != "" {
		exclude, err = regexp.Compile(suite.ExcludePackageRegex)
		if err != nil {
			return nil, fmt.Errorf("exclude_package_regex: %w", err)
		}
	}
	enc := json.NewEncoder(out)
	var records []Record
	for _, pr := range suite.PRs {
		start := time.Now()
		log.Printf("PR %d: start", pr.Number)
		rec := Record{Repo: suite.Repo, PR: pr.Number, Title: pr.Title}
		func() {
			head := pr.Head
			if head == "" {
				h, err := FetchPRHead(ctx, suite.Workdir, pr.Number)
				if err != nil {
					rec.Error = "fetch PR head: " + err.Error()
					return
				}
				head = h
			}
			rec.Head = head
			if err := Checkout(ctx, suite.Workdir, head); err != nil {
				rec.Error = "checkout: " + err.Error()
				return
			}
			base := pr.Base
			if base == "" {
				b, err := MergeBase(ctx, suite.Workdir, "origin/HEAD", head)
				if err != nil {
					rec.Error = "merge-base: " + err.Error()
					return
				}
				base = b
			}
			rec.Base = base

			pkgs, err := resolvePackages(ctx, suite.Workdir, suite.Packages, exclude)
			if err != nil {
				rec.Error = "resolve packages: " + err.Error()
				return
			}
			log.Printf("PR %d: %d packages in test universe", pr.Number, len(pkgs))

			// Plan every strategy at the head checkout.
			sels := make([]Selection, 0, len(strategies))
			for _, spec := range strategies {
				sc := cfg
				if spec.noDirect {
					sc.Safety.AlwaysRunDirectDependents = false
				}
				var scorer semantic.Scorer
				if spec.st == policy.StrategyJevCI && scorerFactory != nil {
					scorer = scorerFactory()
				}
				sel, err := PlanSelection(ctx, suite.Workdir, base, head, spec.st, sc, scorer)
				if err != nil {
					sel = Selection{Strategy: spec.label, Error: err.Error()}
				}
				sel.Strategy = spec.label
				sels = append(sels, sel)
				log.Printf("PR %d: planned %s (%d selected, %s)", pr.Number, spec.label, len(sel.Selected), time.Since(start).Round(time.Second))
			}

			var results map[string]PackageResult
			var full FullSuite
			var oracle Oracle
			if suite.RunTests {
				log.Printf("PR %d: running full suite at head", pr.Number)
				ts := time.Now()
				results, err = RunFullSuite(ctx, suite.Workdir, pkgs, suite.TestTimeout, suite.TestArgs)
				if err != nil {
					rec.Error = "go test: " + err.Error()
					return
				}
				full = SummarizeFullSuite(results)
				rec.FullSuite = full
				log.Printf("PR %d: head suite done in %s (%d failed)", pr.Number, time.Since(ts).Round(time.Second), len(full.Failed))

				if suite.Oracle == "revert" {
					oracle = runRevertOracle(ctx, suite.Workdir, base, head, pkgs, suite, results, &rec)
				} else {
					oracle = HeadOracle(full)
				}
				rec.Oracle = oracle
			}

			for _, sel := range sels {
				if results != nil {
					sel = RestrictToTested(sel, results)
				}
				rec.Strategies = append(rec.Strategies, Compute(sel, full, oracle, results))
			}
		}()
		records = append(records, rec)
		enc.Encode(rec)
		log.Printf("PR %d: done in %s%s", pr.Number, time.Since(start).Round(time.Second), errSuffix(rec.Error))
	}
	return records, nil
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " (error: " + e + ")"
}

// resolvePackages expands go package patterns via `go list` in dir and
// applies the optional exclusion regex.
func resolvePackages(ctx context.Context, dir string, patterns []string, exclude *regexp.Regexp) ([]string, error) {
	args := append([]string{"list"}, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (exclude != nil && exclude.MatchString(line)) {
			continue
		}
		pkgs = append(pkgs, line)
	}
	return pkgs, nil
}

// revertSet computes which files to restore from base and which to remove
// for the revert oracle. Only non-test .go files outside vendor/ and
// testdata/ count. Renames restore the old path and remove the new one.
func revertSet(changes []gitdiff.FileChange) (restore, remove []string) {
	src := func(p string) bool {
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return false
		}
		return !strings.HasPrefix(p, "vendor/") && !strings.Contains(p, "/vendor/") &&
			!strings.HasPrefix(p, "testdata/") && !strings.Contains(p, "/testdata/")
	}
	for _, c := range changes {
		switch c.Status {
		case gitdiff.Modified, gitdiff.TypeChanged:
			if src(c.Path) {
				restore = append(restore, c.Path)
			}
		case gitdiff.Deleted:
			if src(c.Path) {
				restore = append(restore, c.Path)
			}
		case gitdiff.Added:
			if src(c.Path) {
				remove = append(remove, c.Path)
			}
		case gitdiff.Renamed:
			if src(c.OldPath) {
				restore = append(restore, c.OldPath)
			}
			if src(c.Path) {
				remove = append(remove, c.Path)
			}
		case gitdiff.Copied:
			if src(c.Path) {
				remove = append(remove, c.Path)
			}
		}
	}
	return restore, remove
}

// runRevertOracle reverts the PR's non-test source files to base, reruns the
// suite, and returns the oracle. The workdir is reset back to head
// afterwards.
func runRevertOracle(ctx context.Context, workdir, base, head string, pkgs []string, suite Suite, headResults map[string]PackageResult, rec *Record) Oracle {
	repo, err := gitdiff.Open(workdir)
	if err != nil {
		rec.Error = "revert oracle: " + err.Error()
		return HeadOracle(SummarizeFullSuite(headResults))
	}
	changes, err := repo.Changes(base, head)
	if err != nil {
		rec.Error = "revert oracle changes: " + err.Error()
		return HeadOracle(SummarizeFullSuite(headResults))
	}
	restore, remove := revertSet(changes)
	if len(restore)+len(remove) == 0 {
		log.Printf("PR %d: no source files to revert; using head oracle", rec.PR)
		o := HeadOracle(SummarizeFullSuite(headResults))
		o.Method = "head_failures"
		return o
	}
	log.Printf("PR %d: reverting %d files, removing %d", rec.PR, len(restore), len(remove))
	for _, f := range restore {
		if _, err := git(ctx, workdir, "checkout", base, "--", f); err != nil {
			rec.Error = "revert oracle checkout: " + err.Error()
			resetWorkdir(ctx, workdir, head)
			return HeadOracle(SummarizeFullSuite(headResults))
		}
	}
	for _, f := range remove {
		if _, err := git(ctx, workdir, "rm", "-q", "-f", "--", f); err != nil {
			rec.Error = "revert oracle rm: " + err.Error()
			resetWorkdir(ctx, workdir, head)
			return HeadOracle(SummarizeFullSuite(headResults))
		}
	}
	ts := time.Now()
	revertResults, err := RunFullSuite(ctx, workdir, pkgs, suite.TestTimeout, suite.TestArgs)
	if err != nil {
		rec.Error = "revert oracle go test: " + err.Error()
		resetWorkdir(ctx, workdir, head)
		return HeadOracle(SummarizeFullSuite(headResults))
	}
	log.Printf("PR %d: revert suite done in %s", rec.PR, time.Since(ts).Round(time.Second))
	if err := resetWorkdir(ctx, workdir, head); err != nil {
		rec.Error = "reset workdir: " + err.Error()
	}
	return RevertOracle(revertResults, headResults)
}

func resetWorkdir(ctx context.Context, dir, head string) error {
	if _, err := git(ctx, dir, "reset", "-q", "--hard", head); err != nil {
		return err
	}
	_, err := git(ctx, dir, "clean", "-qfd")
	return err
}

// LoadRecords reads a JSONL results file.
func LoadRecords(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Record
	dec := json.NewDecoder(f)
	for dec.More() {
		var r Record
		if err := dec.Decode(&r); err != nil {
			return out, fmt.Errorf("decode %s: %w", path, err)
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PR < out[j].PR })
	return out, nil
}
