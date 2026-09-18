// Package planner orchestrates git diff, go list, the dependency graph,
// Jev scoring and policy into a Plan.
package planner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/dependency"
	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
	"github.com/abdelrahmanmagdii/jevci/internal/golang"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

// Options configures a Plan run.
type Options struct {
	RepoDir  string
	Base     string
	Head     string
	Strategy policy.Strategy
	Config   config.Config
	Scorer   semantic.Scorer // nil = disabled
}

// Target is one test package with its classification and decision.
type Target struct {
	Package    string
	ImportPath string
	Class      policy.StaticClass
	Distance   int
	Via        []string
	Tests      []string
	Decision   policy.Decision
	Score      *float64
	ScoreErr   string
}

// Plan is the full impact analysis result.
type Plan struct {
	Base, Head, MergeBase string
	Strategy              string
	ChangedFiles          []string
	IgnoredFiles          []string
	UnattributedFiles     []string
	NestedModuleFiles     []string
	ChangedPackages       []string
	RunAllReason          string
	Targets               []Target
	Selected              []string
	Skipped               []string
	TargetsTotal          int
	TargetsSelected       int
	ReductionPercent      float64
	Jev                   struct {
		Enabled                           bool
		Model                             string
		Requests, InputTokens, Candidates int
		LatencyMS                         int64
		Error                             string
	}
	Timings struct {
		GitMS, GoListMS, JevMS, TotalMS int64
	}
}

var moduleFiles = map[string]string{
	"go.mod":             "go.mod changed",
	"go.sum":             "go.sum changed",
	"go.work":            "go.work changed",
	"go.work.sum":        "go.work.sum changed",
	"vendor/modules.txt": "vendor/modules.txt changed",
}

// Run computes the impact plan.
func Run(ctx context.Context, o Options) (*Plan, error) {
	total := time.Now()
	p := &Plan{Strategy: o.Strategy.String()}
	if o.Head == "" {
		o.Head = "HEAD"
	}
	cfg := o.Config

	repo, err := gitdiff.Open(o.RepoDir)
	if err != nil {
		return nil, err
	}
	gitStart := time.Now()
	head, err := repo.ResolveRev(o.Head)
	if err != nil {
		return nil, err
	}
	p.Head = head
	p.Base = o.Base
	mb, err := repo.MergeBase(o.Base, head)
	if err != nil {
		return nil, err
	}
	p.MergeBase = mb
	changes, err := repo.Changes(mb, head)
	if err != nil {
		return nil, err
	}

	kept, ignored := splitIgnored(changes, cfg.Safety.IgnoreFiles)
	for _, c := range kept {
		p.ChangedFiles = append(p.ChangedFiles, c.Path)
	}
	p.IgnoredFiles = ignored

	goStart := time.Now()
	ws, err := golang.List(ctx, repo.Dir)
	if err != nil {
		return nil, err
	}
	p.Timings.GoListMS = time.Since(goStart).Milliseconds()

	changedPkgs := map[string]bool{}
	changedTestPkgs := map[string]bool{}
	unattributed := map[string]bool{}
	nestedSet := map[string]bool{}
	for _, c := range kept {
		attribute(c.Path, cfg, ws, changedPkgs, changedTestPkgs, unattributed, nestedSet, p)
		if c.Status == gitdiff.Renamed || c.Status == gitdiff.Copied {
			attribute(c.OldPath, cfg, ws, changedPkgs, changedTestPkgs, unattributed, nestedSet, p)
		}
	}
	for f := range nestedSet {
		p.NestedModuleFiles = append(p.NestedModuleFiles, f)
	}
	sort.Strings(p.NestedModuleFiles)
	if len(p.NestedModuleFiles) > 0 && cfg.Safety.NestedModules == config.NestedModulesRunAll && p.RunAllReason == "" {
		p.RunAllReason = policy.ReasonNestedModule
	}
	for f := range unattributed {
		p.UnattributedFiles = append(p.UnattributedFiles, f)
	}
	sort.Strings(p.UnattributedFiles)
	if len(p.UnattributedFiles) > 0 && cfg.Safety.UnattributedFiles == config.UnattributedRunAll && p.RunAllReason == "" {
		p.RunAllReason = policy.ReasonUnattributed
	}
	for ip := range changedPkgs {
		p.ChangedPackages = append(p.ChangedPackages, ws.Rel(ip))
	}
	sort.Strings(p.ChangedPackages)
	p.Timings.GitMS = time.Since(gitStart).Milliseconds() - p.Timings.GoListMS

	graph := dependency.New(ws)
	var changedRoots []string
	for ip := range changedPkgs {
		changedRoots = append(changedRoots, ip)
	}
	impacts := graph.Dependents(changedRoots)

	for _, pkg := range ws.Packages {
		if !pkg.HasTests() {
			continue
		}
		tg := classify(ws, graph, impacts, changedPkgs, changedTestPkgs, cfg, pkg)
		tests, err := golang.TestFunctions(pkg)
		if err == nil {
			for _, tf := range tests {
				tg.Tests = append(tg.Tests, tf.Name)
			}
		}
		p.Targets = append(p.Targets, tg)
	}
	sort.Slice(p.Targets, func(i, j int) bool { return p.Targets[i].Package < p.Targets[j].Package })

	runAll := p.RunAllReason != ""
	scores, scoreErrs := p.score(ctx, o, cfg, repo, mb, head, kept, ws, runAll)

	for i := range p.Targets {
		tg := &p.Targets[i]
		var se error
		if e, ok := scoreErrs[tg.ImportPath]; ok {
			se = e
			tg.ScoreErr = e.Error()
		}
		tg.Score = scores[tg.ImportPath]
		root := ""
		if imp, ok := impacts[tg.ImportPath]; ok && imp.Root != "" {
			root = ws.Rel(imp.Root)
		}
		tg.Decision = policy.Decide(cfg.Safety, cfg.Jev, policy.Input{
			Class: tg.Class, Distance: tg.Distance, Score: tg.Score,
			ScoreErr: se, RunAll: runAll, RunAllReason: p.RunAllReason,
			Strategy: o.Strategy, ViaRoot: root,
		})
		if tg.Decision.Action == policy.Skip {
			p.Skipped = append(p.Skipped, tg.Package)
		} else {
			p.Selected = append(p.Selected, tg.Package)
		}
	}
	p.TargetsTotal = len(p.Targets)
	p.TargetsSelected = len(p.Selected)
	if p.TargetsTotal > 0 {
		p.ReductionPercent = round1((1 - float64(p.TargetsSelected)/float64(p.TargetsTotal)) * 100)
	}
	p.Timings.TotalMS = time.Since(total).Milliseconds()
	return p, nil
}

func splitIgnored(changes []gitdiff.FileChange, patterns []string) (kept []gitdiff.FileChange, ignored []string) {
	seen := map[string]bool{}
	for _, c := range changes {
		match := false
		if !underTestdata(c.Path) {
			for _, pat := range patterns {
				if config.MatchGlob(pat, c.Path) || (c.OldPath != "" && config.MatchGlob(pat, c.OldPath)) {
					match = true
					break
				}
			}
		}
		if match {
			if !seen[c.Path] {
				seen[c.Path] = true
				ignored = append(ignored, c.Path)
			}
			continue
		}
		kept = append(kept, c)
	}
	return kept, ignored
}

// underTestdata reports whether a repo-relative path lives inside a
// testdata directory; ignore globs never apply there.
func underTestdata(path string) bool {
	p := filepath.ToSlash(path)
	return strings.HasPrefix(p, "testdata/") || strings.Contains(p, "/testdata/")
}

func attribute(path string, cfg config.Config, ws *golang.Workspace, changedPkgs, changedTestPkgs, unattributed, nested map[string]bool, p *Plan) {
	if path == "" {
		return
	}
	for _, dir := range ws.NestedModules {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			nested[path] = true
			return
		}
	}
	if reason, ok := moduleFiles[path]; ok {
		if cfg.Safety.RunAllOnModuleChange && p.RunAllReason == "" {
			p.RunAllReason = reason
		}
		return
	}
	pkg, ok := ws.PackageForFile(path)
	if !ok {
		unattributed[path] = true
		return
	}
	if strings.HasSuffix(path, "_test.go") {
		changedTestPkgs[pkg.ImportPath] = true
		return
	}
	changedPkgs[pkg.ImportPath] = true
}

func classify(ws *golang.Workspace, graph *dependency.Graph, impacts map[string]dependency.Impact, changedPkgs, changedTestPkgs map[string]bool, cfg config.Config, pkg *golang.Package) Target {
	rel := ws.Rel(pkg.ImportPath)
	tg := Target{Package: rel, ImportPath: pkg.ImportPath}
	imp, ok := impacts[pkg.ImportPath]
	switch {
	case changedPkgs[pkg.ImportPath]:
		tg.Class = policy.ClassChangedPackage
	case changedTestPkgs[pkg.ImportPath]:
		tg.Class = policy.ClassChangedTest
	case isProtected(cfg.Safety.ProtectedTests, rel):
		tg.Class = policy.ClassProtected
	case ok && imp.Distance == 1:
		tg.Class = policy.ClassDirectDependent
		tg.Distance = 1
	case ok && imp.Distance >= 2:
		tg.Class = policy.ClassTransitiveDependent
		tg.Distance = imp.Distance
	default:
		tg.Class = policy.ClassUnrelated
	}
	if ok && imp.Distance > 0 {
		for _, step := range graph.Path(pkg.ImportPath) {
			tg.Via = append(tg.Via, ws.Rel(step))
		}
	}
	return tg
}

// score runs the scorer over the Jev candidates and returns per-import-path
// scores and errors.
func (p *Plan) score(ctx context.Context, o Options, cfg config.Config, repo *gitdiff.Repo, mb, head string, kept []gitdiff.FileChange, ws *golang.Workspace, runAll bool) (map[string]*float64, map[string]error) {
	scores := map[string]*float64{}
	errs := map[string]error{}
	if o.Strategy == policy.StrategyJevCI && o.Scorer != nil {
		p.Jev.Enabled = true
	}
	if o.Strategy != policy.StrategyJevCI || o.Scorer == nil || runAll {
		return scores, errs
	}
	var candIdx []int
	for i, tg := range p.Targets {
		if tg.Class == policy.ClassTransitiveDependent ||
			(tg.Class == policy.ClassDirectDependent && !cfg.Safety.AlwaysRunDirectDependents) ||
			(tg.Class == policy.ClassUnrelated && cfg.Jev.Candidates == config.CandidatesAllUnselected) {
			candIdx = append(candIdx, i)
		}
	}
	p.Jev.Candidates = len(candIdx)
	if len(candIdx) == 0 {
		return scores, errs
	}

	var candidates []semantic.Candidate
	for _, i := range candIdx {
		tg := p.Targets[i]
		pkg := ws.Packages[tg.ImportPath]
		c := semantic.Candidate{
			ID:           tg.ImportPath,
			Package:      tg.Package,
			ImportPath:   tg.ImportPath,
			Relationship: relationship(tg),
			PackageDoc:   golang.PackageDoc(pkg),
		}
		for _, f := range append(append([]string{}, pkg.TestGoFiles...), pkg.XTestGoFiles...) {
			c.TestFiles = append(c.TestFiles, tg.Package+"/"+f)
		}
		c.Tests = capList(tg.Tests, 40)
		candidates = append(candidates, c)
	}

	change := buildChange(repo, mb, head, kept, ws, p, cfg)
	jevStart := time.Now()
	results, err := o.Scorer.Score(ctx, change, candidates)
	p.Timings.JevMS = time.Since(jevStart).Milliseconds()
	if err != nil {
		p.Jev.Error = err.Error()
	}
	for i, r := range results {
		ip := p.Targets[candIdx[i]].ImportPath
		if r.Err != nil {
			errs[ip] = r.Err
			continue
		}
		if r.Score != nil {
			v := r.Score.Probability
			scores[ip] = &v
			if r.Score.Model != "" {
				p.Jev.Model = r.Score.Model
			}
		}
	}
	if u, ok := o.Scorer.(interface{ Usage() semantic.Usage }); ok {
		us := u.Usage()
		p.Jev.Requests = us.Requests
		p.Jev.InputTokens = us.InputTokens
		if us.Model != "" {
			p.Jev.Model = us.Model
		}
		p.Jev.LatencyMS = us.Latency.Milliseconds()
	}
	if p.Jev.LatencyMS == 0 {
		p.Jev.LatencyMS = p.Timings.JevMS
	}
	return scores, errs
}

func relationship(tg Target) string {
	switch {
	case len(tg.Via) == 0:
		return "no import dependency on changed packages"
	case tg.Distance == 1:
		return fmt.Sprintf("imports changed package %s (distance 1)", tg.Via[0])
	default:
		return fmt.Sprintf("imports %s, which imports changed package %s (distance %d)",
			tg.Via[len(tg.Via)-1], tg.Via[0], tg.Distance)
	}
}

func capList(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	out := append([]string{}, in[:n]...)
	return append(out, fmt.Sprintf("... and %d more", len(in)-n))
}

func buildChange(repo *gitdiff.Repo, mb, head string, kept []gitdiff.FileChange, ws *golang.Workspace, p *Plan, cfg config.Config) semantic.Change {
	ch := semantic.Change{Base: mb, Head: head}
	ch.ChangedPackages = p.ChangedPackages
	for _, c := range kept {
		ch.ChangedFiles = append(ch.ChangedFiles, c.Path)
	}

	// Diff: changed .go files first, then other files; budget max_diff_chars.
	var goFiles, other []string
	for _, f := range ch.ChangedFiles {
		if strings.HasSuffix(f, ".go") {
			goFiles = append(goFiles, f)
		} else {
			other = append(other, f)
		}
	}
	var diff strings.Builder
	remaining := cfg.Jev.MaxDiffChars
	for _, group := range [][]string{goFiles, other} {
		for _, f := range group {
			if remaining <= 0 {
				break
			}
			d, err := repo.Diff(mb, head, []string{f}, cfg.Jev.DiffContextLines)
			if err != nil {
				continue
			}
			if len(d) > remaining {
				d = d[:remaining] + "\n... [diff truncated]"
			}
			diff.WriteString(d)
			remaining -= len(d)
		}
	}
	ch.Diff = diff.String()

	// Symbols: extract changed decls on both sides from an untruncated -U0
	// diff of all changed .go files (the Jev-state diff above is budgeted
	// and must not drive symbol extraction).
	symDiff, err := repo.Diff(mb, head, goFiles, 0)
	if err == nil && symDiff != "" {
		hunks := gitdiff.ParseHunks(symDiff)
		symSeen := map[string]bool{}
		var overflow int
		for f, hs := range hunks {
			if !strings.HasSuffix(f, ".go") || len(hs) == 0 {
				continue
			}
			pkgRel := ws.Rel(fileImportDir(ws, f))
			var names []string
			if src, err := repo.ShowFile(mb, f); err == nil {
				names = append(names, golang.ChangedSymbols(src, hs, golang.Old)...)
			}
			if src, err := repo.ShowFile(head, f); err == nil {
				names = append(names, golang.ChangedSymbols(src, hs, golang.New)...)
			}
			for _, n := range names {
				key := pkgRel + ": " + n
				if !symSeen[key] {
					symSeen[key] = true
					if len(ch.ChangedSymbols) < 200 {
						ch.ChangedSymbols = append(ch.ChangedSymbols, key)
					} else {
						overflow++
					}
				}
			}
		}
		sort.Strings(ch.ChangedSymbols)
		if overflow > 0 {
			ch.ChangedSymbols = append(ch.ChangedSymbols, fmt.Sprintf("... and %d more", overflow))
		}
	}
	return ch
}

// fileImportDir returns the workspace import path of the directory holding
// repo-relative file f, or "" if none.
func fileImportDir(ws *golang.Workspace, f string) string {
	if pkg, ok := ws.PackageForFile(f); ok {
		return pkg.ImportPath
	}
	return ws.ModulePath
}

func isProtected(patterns []string, rel string) bool {
	for _, pat := range patterns {
		if config.MatchPackagePattern(pat, rel) {
			return true
		}
	}
	return false
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
