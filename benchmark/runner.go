package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
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
	Repo       string   `yaml:"repo"`    // github.com/owner/name or URL
	Workdir    string   `yaml:"workdir"` // clone destination
	PRs        []PRSpec `yaml:"prs"`
	Strategies []string `yaml:"strategies,omitempty"` // default full,changed,static,jevci
	RunTests   bool     `yaml:"run_tests"`
	TestArgs   []string `yaml:"test_args,omitempty"`
}

// LoadSuite reads a suite YAML file.
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
	return s, nil
}

// ScorerFactory returns a fresh scorer per strategy run (isolated usage
// accounting). It may return nil.
type ScorerFactory func() semantic.Scorer

// Run executes the suite: per PR it checks out head, plans every strategy,
// optionally runs the full test suite, and writes one JSONL record per PR
// as it completes.
func Run(ctx context.Context, suite Suite, cfg config.Config, scorerFactory ScorerFactory, out io.Writer) ([]Record, error) {
	if err := EnsureClone(ctx, suite.Repo, suite.Workdir); err != nil {
		return nil, fmt.Errorf("clone %s: %w", suite.Repo, err)
	}
	var strategies []policy.Strategy
	for _, s := range suite.Strategies {
		st, err := policy.ParseStrategy(s)
		if err != nil {
			return nil, err
		}
		strategies = append(strategies, st)
	}
	enc := json.NewEncoder(out)
	var records []Record
	for _, pr := range suite.PRs {
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

			var results map[string]PackageResult
			var full FullSuite
			if suite.RunTests {
				r, err := RunFullSuite(ctx, suite.Workdir, suite.TestArgs)
				if err != nil {
					rec.Error = "go test: " + err.Error()
					return
				}
				results = r
				full = SummarizeFullSuite(results)
				rec.FullSuite = full
			}

			for _, st := range strategies {
				var scorer semantic.Scorer
				if st == policy.StrategyJevCI && scorerFactory != nil {
					scorer = scorerFactory()
				}
				sel, err := PlanSelection(ctx, suite.Workdir, base, head, st, cfg, scorer)
				if err != nil {
					rec.Strategies = append(rec.Strategies, StrategyMetrics{
						Strategy: st.String(), Error: err.Error(),
						FailedDetected: []string{}, FailedMissed: []string{},
					})
					continue
				}
				rec.Strategies = append(rec.Strategies, Compute(sel, full, results))
			}
		}()
		records = append(records, rec)
		enc.Encode(rec)
	}
	return records, nil
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
