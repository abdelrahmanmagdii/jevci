package benchmark

import (
	"testing"
	"time"
)

func results() map[string]PackageResult {
	return map[string]PackageResult{
		"m/a": {ImportPath: "m/a", Passed: true, Elapsed: 2 * time.Second},
		"m/b": {ImportPath: "m/b", Passed: false, Elapsed: 4 * time.Second, FailedTests: []string{"TestB"}},
		"m/c": {ImportPath: "m/c", Passed: false, Elapsed: 1 * time.Second, FailedTests: []string{"TestC"}},
		"m/d": {ImportPath: "m/d", Passed: true, Elapsed: 3 * time.Second},
		"m/e": {ImportPath: "m/e", Skipped: true},
	}
}

func TestSummarizeFullSuite(t *testing.T) {
	fs := SummarizeFullSuite(results())
	if fs.Packages != 4 || fs.RuntimeMS != 10000 {
		t.Fatalf("packages=%d runtime=%d", fs.Packages, fs.RuntimeMS)
	}
	if len(fs.Failed) != 2 || fs.Failed[0] != "m/b" || fs.Failed[1] != "m/c" {
		t.Fatalf("failed=%v", fs.Failed)
	}
}

func TestCompute(t *testing.T) {
	res := results()
	full := SummarizeFullSuite(res)
	sel := Selection{Strategy: "jevci", Selected: []string{"m/a", "m/b", "m/e"}, TargetsTotal: 4, JevInputTokens: 1_000_000}
	m := Compute(sel, full, HeadOracle(full), res)
	if m.Selected != 3 || m.ReductionPercent != 25.0 {
		t.Fatalf("selected=%d reduction=%v", m.Selected, m.ReductionPercent)
	}
	if m.SelectedRuntimeMS != 6000 || m.RuntimeReductionPercent != 40.0 {
		t.Fatalf("runtime=%d reduction=%v", m.SelectedRuntimeMS, m.RuntimeReductionPercent)
	}
	if len(m.FailedDetected) != 1 || m.FailedDetected[0] != "m/b" || len(m.FailedMissed) != 1 || m.FailedMissed[0] != "m/c" {
		t.Fatalf("detected=%v missed=%v", m.FailedDetected, m.FailedMissed)
	}
	if m.Recall == nil || *m.Recall != 0.5 {
		t.Fatalf("recall=%v", m.Recall)
	}
	if m.JevCostUSD != PricePerMillionInputTokensUSD {
		t.Fatalf("cost=%v", m.JevCostUSD)
	}
}

func TestComputeNoSuiteNoFailures(t *testing.T) {
	m := Compute(Selection{Strategy: "static", Selected: []string{"m/a"}, TargetsTotal: 2}, FullSuite{}, Oracle{}, nil)
	if m.Recall != nil || m.SelectedRuntimeMS != 0 || m.ReductionPercent != 50.0 {
		t.Fatalf("unexpected %+v", m)
	}
	full := FullSuite{Ran: true, RuntimeMS: 100, Failed: []string{}}
	m = Compute(Selection{Strategy: "static", TargetsTotal: 0}, full, HeadOracle(full), map[string]PackageResult{})
	if m.Recall != nil || m.ReductionPercent != 0 {
		t.Fatalf("unexpected %+v", m)
	}
}

func TestRevertOracle(t *testing.T) {
	head := results() // m/b and m/c fail at head (noise)
	revert := map[string]PackageResult{
		"m/a": {ImportPath: "m/a", Passed: false, Elapsed: time.Second}, // regression exposed by revert
		"m/b": {ImportPath: "m/b", Passed: false, Elapsed: time.Second}, // fails at head too → noise
		"m/c": {ImportPath: "m/c", Passed: true},
		"m/d": {ImportPath: "m/d", Passed: true},
		"m/e": {ImportPath: "m/e", Passed: false, Elapsed: time.Second}, // no tests at head → excluded
		"m/f": {ImportPath: "m/f", Passed: false, Elapsed: time.Second}, // absent at head (added by PR) → excluded
	}
	o := RevertOracle(revert, head)
	if !o.Ran || o.Method != "revert" {
		t.Fatalf("%+v", o)
	}
	if len(o.Failed) != 1 || o.Failed[0] != "m/a" {
		t.Fatalf("failed=%v", o.Failed)
	}
	if len(o.Noise) != 1 || o.Noise[0] != "m/b" {
		t.Fatalf("noise=%v", o.Noise)
	}
	// Recall against the oracle, not against head failures.
	full := SummarizeFullSuite(head)
	m := Compute(Selection{Strategy: "jevci", Selected: []string{"m/b"}, TargetsTotal: 4}, full, o, head)
	if m.Recall == nil || *m.Recall != 0 || len(m.FailedMissed) != 1 || m.FailedMissed[0] != "m/a" {
		t.Fatalf("%+v", m)
	}
	m = Compute(Selection{Strategy: "static", Selected: []string{"m/a", "m/b"}, TargetsTotal: 4}, full, o, head)
	if m.Recall == nil || *m.Recall != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestRestrictToTested(t *testing.T) {
	res := results() // a,b,c,d tested; e skipped
	sel := Selection{Strategy: "jevci", Selected: []string{"m/a", "m/e", "m/zzz-not-tested"}, TargetsTotal: 40}
	r := RestrictToTested(sel, res)
	if r.TargetsTotal != 4 || len(r.Selected) != 1 || r.Selected[0] != "m/a" {
		t.Fatalf("%+v", r)
	}
	if u := RestrictToTested(sel, nil); u.TargetsTotal != 40 || len(u.Selected) != 3 {
		t.Fatalf("nil results must be a no-op: %+v", u)
	}
}

func TestAggregate(t *testing.T) {
	half := 0.5
	one := 1.0
	recs := []Record{
		{PR: 1, FullSuite: FullSuite{Ran: true, RuntimeMS: 100, Failed: []string{"m/b", "m/c"}}, Oracle: Oracle{Ran: true, Method: "head_failures", Failed: []string{"m/b", "m/c"}}, Strategies: []StrategyMetrics{
			{Strategy: "jevci", ReductionPercent: 60, RuntimeReductionPercent: 40, FailedDetected: []string{"m/b"}, FailedMissed: []string{"m/c"}, Recall: &half, JevLatencyMS: 1000, JevCostUSD: 0.01},
			{Strategy: "full", ReductionPercent: 0, RuntimeReductionPercent: 0, FailedDetected: []string{"m/b", "m/c"}, FailedMissed: []string{}, Recall: &one},
		}},
		{PR: 2, FullSuite: FullSuite{Ran: true, RuntimeMS: 100}, Strategies: []StrategyMetrics{
			{Strategy: "jevci", ReductionPercent: 80, RuntimeReductionPercent: 60, FailedDetected: []string{}, FailedMissed: []string{}, JevLatencyMS: 2000, JevCostUSD: 0.02},
			{Strategy: "full", FailedDetected: []string{}, FailedMissed: []string{}},
		}},
		{PR: 3, Error: "clone failed", Strategies: []StrategyMetrics{{Strategy: "jevci", ReductionPercent: 100}}},
	}
	sums := Aggregate(recs)
	if len(sums) != 2 || sums[0].Strategy != "jevci" || sums[1].Strategy != "full" {
		t.Fatalf("order/len: %+v", sums)
	}
	j := sums[0]
	if j.PRs != 2 || j.MeanReductionPercent != 70 || j.MeanRuntimeReductionPct != 50 {
		t.Fatalf("jevci summary %+v", j)
	}
	if j.FailedTotal != 2 || j.FailedDetected != 1 || j.FailedMissed != 1 || j.Recall != 0.5 || j.PRsWithFailures != 1 {
		t.Fatalf("jevci recall %+v", j)
	}
	if j.MeanJevLatencyMS != 1500 || j.TotalJevCostUSD < 0.0299 || j.TotalJevCostUSD > 0.0301 {
		t.Fatalf("jevci latency/cost %+v", j)
	}
	if f := sums[1]; f.Recall != 1 || f.FailedTotal != 2 || f.FailedMissed != 0 {
		t.Fatalf("full summary %+v", f)
	}
}
