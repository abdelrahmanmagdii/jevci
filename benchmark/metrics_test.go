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
	m := Compute(sel, full, res)
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
	m := Compute(Selection{Strategy: "static", Selected: []string{"m/a"}, TargetsTotal: 2}, FullSuite{}, nil)
	if m.Recall != nil || m.SelectedRuntimeMS != 0 || m.ReductionPercent != 50.0 {
		t.Fatalf("unexpected %+v", m)
	}
	full := FullSuite{Ran: true, RuntimeMS: 100, Failed: []string{}}
	m = Compute(Selection{Strategy: "static", TargetsTotal: 0}, full, map[string]PackageResult{})
	if m.Recall != nil || m.ReductionPercent != 0 {
		t.Fatalf("unexpected %+v", m)
	}
}

func TestAggregate(t *testing.T) {
	half := 0.5
	one := 1.0
	recs := []Record{
		{PR: 1, FullSuite: FullSuite{Ran: true, RuntimeMS: 100, Failed: []string{"m/b", "m/c"}}, Strategies: []StrategyMetrics{
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
