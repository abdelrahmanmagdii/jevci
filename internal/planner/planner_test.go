package planner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
	"github.com/abdelrahmanmagdii/jevci/internal/testutil"
)

func baseOpts(dir string) Options {
	return Options{
		RepoDir:  dir,
		Base:     "HEAD~1",
		Head:     "HEAD",
		Strategy: policy.StrategyJevCI,
		Config:   config.Default(),
	}
}

func targetByPkg(p *Plan, rel string) *Target {
	for i := range p.Targets {
		if p.Targets[i].Package == rel {
			return &p.Targets[i]
		}
	}
	return nil
}

func TestPlanCoreChange(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/core.go", `// Package core provides the key-value store used across the fixture.
package core

// Store is an in-memory key-value store.
type Store struct {
	m map[string]string
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{m: map[string]string{}}
}

// Get returns a value by key.
func (s *Store) Get(k string) string {
	return s.m[k]
}

// Set stores a value.
func (s *Store) Set(k, v string) {
	s.m[k] = v
}

// Delete removes a key.
func (s *Store) Delete(k string) {
	delete(s.m, k)
}
`)
	commit("add delete")
	o := baseOpts(dir)
	o.Scorer = &semantic.MockScorer{Scores: map[string]float64{
		"example.com/fixture/web":     0.9,
		"example.com/fixture/metrics": 0.1,
	}}
	p, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ChangedPackages; len(got) != 1 || got[0] != "./core" {
		t.Fatalf("changed packages %v", got)
	}
	check := func(rel string, want policy.Action, src policy.Source) {
		tg := targetByPkg(p, rel)
		if tg == nil {
			t.Fatalf("no target %s", rel)
		}
		if tg.Decision.Action != want || tg.Decision.Source != src {
			t.Fatalf("%s: %v/%v want %v/%v", rel, tg.Decision.Action, tg.Decision.Source, want, src)
		}
	}
	check("./core", policy.Run, policy.SourceChanged)
	check("./api", policy.Run, policy.SourceStatic)
	check("./web", policy.Run, policy.SourceJev)
	check("./metrics", policy.Skip, policy.SourceJev)
	check("./unrelated", policy.Skip, policy.SourceUnrelated)
	if p.Jev.Candidates != 2 {
		t.Fatalf("candidates=%d", p.Jev.Candidates)
	}
}

func TestPlanTestOnlyChange(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "api/api_test.go", `package api

import (
	"testing"

	"example.com/fixture/core"
)

func TestLookup(t *testing.T) {
	h := New(core.NewStore())
	if h.Lookup("x") != "" {
		t.Fatal("want empty")
	}
}

func TestLookupEmpty(t *testing.T) {
	h := New(core.NewStore())
	if h.Lookup("y") != "" {
		t.Fatal("want empty")
	}
}
`)
	commit("test only")
	p, err := Run(context.Background(), baseOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ChangedPackages) != 0 {
		t.Fatalf("changed pkgs %v", p.ChangedPackages)
	}
	tg := targetByPkg(p, "./api")
	if tg.Decision.Action != policy.Run || tg.Decision.Source != policy.SourceChangedTest {
		t.Fatalf("api: %v/%v", tg.Decision.Action, tg.Decision.Source)
	}
	// dependents of api must not run
	for _, rel := range []string{"./web", "./metrics"} {
		tg := targetByPkg(p, rel)
		if tg.Decision.Action != policy.Skip {
			t.Fatalf("%s should skip, got %v", rel, tg.Decision)
		}
	}
}

func TestPlanModuleChange(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "go.mod", "module example.com/fixture\n\ngo 1.24\n\n// touch\n")
	commit("touch go.mod")
	p, err := Run(context.Background(), baseOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if p.RunAllReason == "" {
		t.Fatal("want run all reason")
	}
	if p.TargetsSelected != p.TargetsTotal {
		t.Fatalf("selected %d/%d", p.TargetsSelected, p.TargetsTotal)
	}
}

func TestPlanMockErrorFailOpen(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/core.go", `package core

// Store is an in-memory key-value store.
type Store struct{ m map[string]string }

func NewStore() *Store { return &Store{m: map[string]string{}} }

func (s *Store) Get(k string) string { return s.m[k] }

func (s *Store) Set(k, v string) { s.m[k] = v }
`)
	commit("rewrite")
	o := baseOpts(dir)
	o.Scorer = &semantic.MockScorer{Err: errors.New("jev down")}
	p, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if p.Jev.Error == "" {
		t.Fatal("want Jev.Error")
	}
	for _, rel := range []string{"./web", "./metrics"} {
		tg := targetByPkg(p, rel)
		if tg.Decision.Action != policy.RunPackage || tg.Decision.Source != policy.SourceFallback {
			t.Fatalf("%s: %+v", rel, tg.Decision)
		}
	}
}

func TestPlanDirectDependentsScored(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/core.go", `package core

// Store is an in-memory key-value store.
type Store struct{ m map[string]string }

func NewStore() *Store { return &Store{m: map[string]string{}} }

func (s *Store) Get(k string) string { return s.m[k] }

func (s *Store) Set(k, v string) { s.m[k] = v }
`)
	commit("rewrite")
	o := baseOpts(dir)
	o.Config.Safety.AlwaysRunDirectDependents = false
	o.Scorer = &semantic.MockScorer{Scores: map[string]float64{
		"example.com/fixture/api":     0.1,
		"example.com/fixture/web":     0.9,
		"example.com/fixture/metrics": 0.1,
	}}
	p, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	tg := targetByPkg(p, "./api")
	if tg.Decision.Action != policy.Skip || tg.Decision.Source != policy.SourceJev {
		t.Fatalf("api: %+v", tg.Decision)
	}
	if tg := targetByPkg(p, "./web"); tg.Decision.Action != policy.Run || tg.Decision.Source != policy.SourceJev {
		t.Fatalf("web: %+v", tg.Decision)
	}
}

func TestStrategies(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/core.go", `package core

// Store is an in-memory key-value store.
type Store struct{ m map[string]string }

func NewStore() *Store { return &Store{m: map[string]string{}} }

func (s *Store) Get(k string) string { return s.m[k] }

func (s *Store) Set(k, v string) { s.m[k] = v }
`)
	commit("rewrite")

	run := func(st policy.Strategy) *Plan {
		o := baseOpts(dir)
		o.Strategy = st
		p, err := Run(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	full := run(policy.StrategyFull)
	if full.TargetsSelected != full.TargetsTotal {
		t.Fatalf("full: %d/%d", full.TargetsSelected, full.TargetsTotal)
	}
	changed := run(policy.StrategyChanged)
	if changed.TargetsSelected != 1 {
		t.Fatalf("changed: %d", changed.TargetsSelected)
	}
	static := run(policy.StrategyStatic)
	// changed + direct + transitive
	if static.TargetsSelected != 4 {
		t.Fatalf("static: selected %d want 4 (%v)", static.TargetsSelected, static.Selected)
	}
}

func TestReadmeOnly(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "README.md", "# hi\n")
	commit("readme")
	p, err := Run(context.Background(), baseOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.IgnoredFiles) != 1 || p.IgnoredFiles[0] != "README.md" {
		t.Fatalf("ignored %v", p.IgnoredFiles)
	}
	if p.TargetsSelected != 0 {
		t.Fatalf("selected %d", p.TargetsSelected)
	}
}

func TestNestedModuleFiles(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "tools/main.go", `package main

import "fmt"

func main() { fmt.Println("tool v2") }
`)
	commit("tool change")
	p, err := Run(context.Background(), baseOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.NestedModuleFiles) != 1 || p.NestedModuleFiles[0] != "tools/main.go" {
		t.Fatalf("nested %v", p.NestedModuleFiles)
	}
	if p.RunAllReason != "" {
		t.Fatalf("runall %q", p.RunAllReason)
	}
	if p.TargetsSelected != 0 {
		t.Fatalf("selected %d", p.TargetsSelected)
	}
	if len(p.UnattributedFiles) != 0 {
		t.Fatalf("unattributed %v", p.UnattributedFiles)
	}
}

func TestTestdataNeverIgnored(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/testdata/sample.txt", "new sample data\n")
	testutil.WriteFile(t, dir, "docs/x.md", "# docs\n")
	commit("testdata + docs")
	p, err := Run(context.Background(), baseOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ChangedPackages) != 1 || p.ChangedPackages[0] != "./core" {
		t.Fatalf("changed %v", p.ChangedPackages)
	}
	if len(p.IgnoredFiles) != 1 || p.IgnoredFiles[0] != "docs/x.md" {
		t.Fatalf("ignored %v", p.IgnoredFiles)
	}
}

func TestSourceReplayPlan(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/feature.go", "package core\nfunc ReplayValue() int { return 0 }\n")
	commit("replay base")
	testutil.WriteFile(t, dir, "core/feature.go", "package core\nfunc ReplayValue() int { return 1 }\n")
	testutil.WriteFile(t, dir, "api/replay_test.go", "package api\nimport \"testing\"\nfunc TestReplay(t *testing.T) {}\n")
	commit("replay head")
	o := baseOpts(dir)
	o.Strategy = policy.StrategyChanged
	historical, err := Run(context.Background(), o)
	if err != nil || targetByPkg(historical, "./api").Class != policy.ClassChangedTest {
		t.Fatalf("historical classification: %v, %+v", err, historical)
	}
	o.SourceFiles = []string{"core/feature.go"}
	replay, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.ChangedFiles) != 1 || replay.ChangedFiles[0] != "core/feature.go" || targetByPkg(replay, "./api").Class != policy.ClassDirectDependent || targetByPkg(replay, "./api").Decision.Action != policy.Skip {
		t.Fatalf("replay must exclude test changes: %+v", replay)
	}
	if _, _, err := CalibrationInput(context.Background(), o, nil); err == nil {
		t.Fatal("historical calibration must reject source replay inputs")
	}
}

func TestCalibrationInput(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/extra.go", "package core\nfunc Extra() {}\n")
	testutil.WriteFile(t, dir, "api/extra_test.go", "package api\nimport \"testing\"\nfunc TestExtra(t *testing.T) {}\n")
	commit("calibration fixture")
	o := baseOpts(dir)
	mock := &semantic.MockScorer{}
	o.Scorer = mock
	change, candidates, err := CalibrationInput(context.Background(), o, []string{
		"example.com/fixture/core", "example.com/fixture/api", "example.com/fixture/web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mock.Calls != 0 || len(candidates) != 2 || candidates[0].Package != "./api" || candidates[1].Package != "./core" {
		t.Fatalf("calls=%d candidates=%+v", mock.Calls, candidates)
	}
	if candidates[0].Relationship != "this package contains changed test files" || candidates[1].Relationship != "this package contains changed non-test files (distance 0)" {
		t.Fatalf("relationships: %+v", candidates)
	}
	if len(candidates[0].TestFiles) == 0 || len(candidates[0].Tests) == 0 || !strings.Contains(change.Diff, "func Extra()") || len(change.ChangedPackages) != 1 || change.ChangedPackages[0] != "./core" {
		t.Fatalf("change=%+v candidates=%+v", change, candidates)
	}
	_, candidates, err = CalibrationInput(context.Background(), o, []string{"example.com/fixture/core"})
	if err != nil || len(candidates) != 1 || candidates[0].Package != "./core" {
		t.Fatalf("universe restriction: %v %+v", err, candidates)
	}
	_, candidates, err = CalibrationInput(context.Background(), o, nil)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("empty universe: %v %+v", err, candidates)
	}
	o.Head = "HEAD~1"
	if _, _, err := CalibrationInput(context.Background(), o, nil); err == nil {
		t.Fatal("expected checked-out head validation")
	}
}

func TestProtected(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/core.go", `package core

// Store is an in-memory key-value store.
type Store struct{ m map[string]string }

func NewStore() *Store { return &Store{m: map[string]string{}} }

func (s *Store) Get(k string) string { return s.m[k] }

func (s *Store) Set(k, v string) { s.m[k] = v }
`)
	commit("core change")
	o := baseOpts(dir)
	o.Config.Safety.ProtectedTests = []string{"./unrelated"}
	p, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	tg := targetByPkg(p, "./unrelated")
	if tg.Decision.Action != policy.Run || tg.Decision.Source != policy.SourceProtected {
		t.Fatalf("%+v", tg.Decision)
	}
}
