package benchmark

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/testutil"
)

const stream = `{"Time":"2026-01-01T00:00:00Z","Action":"run","Package":"example.com/a","Test":"TestX"}
{"Time":"2026-01-01T00:00:01Z","Action":"pass","Package":"example.com/a","Test":"TestX","Elapsed":0.01}
{"Time":"2026-01-01T00:00:01Z","Action":"pass","Package":"example.com/a","Elapsed":0.5}
{"Time":"2026-01-01T00:00:00Z","Action":"run","Package":"example.com/b","Test":"TestBad"}
{"Time":"2026-01-01T00:00:01Z","Action":"fail","Package":"example.com/b","Test":"TestBad","Elapsed":0.02}
{"Time":"2026-01-01T00:00:01Z","Action":"fail","Package":"example.com/b","Elapsed":0.3}
{"Time":"2026-01-01T00:00:00Z","Action":"output","Package":"example.com/c","Output":"?   \texample.com/c\t[no test files]\n"}
{"Time":"2026-01-01T00:00:00Z","Action":"skip","Package":"example.com/c"}
not json at all
`

func TestParseTestJSON(t *testing.T) {
	res, err := ParseTestJSON([]byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	a := res["example.com/a"]
	if !a.Passed || a.Skipped || a.Elapsed.Milliseconds() != 500 {
		t.Fatalf("a: %+v", a)
	}
	b := res["example.com/b"]
	if b.Passed || len(b.FailedTests) != 1 || b.FailedTests[0] != "TestBad" {
		t.Fatalf("b: %+v", b)
	}
	c := res["example.com/c"]
	if !c.Skipped {
		t.Fatalf("c: %+v", c)
	}
	if _, err := ParseTestJSON([]byte("garbage\n")); err == nil {
		t.Fatal("want error on empty stream")
	}
}

func TestParseTestJSONRejectsIncomplete(t *testing.T) {
	for _, input := range []string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"pass","Package":"p","Test":"TestX"}`,
		`{"Action":"fail","Package":"p","Test":"TestX"}`,
		"{invalid json}",
		"{\"Action\":\"pass\",\"Package\":\"p\"}\n{\"Action\":\"fail\",\"Package\":\"p\"}",
	} {
		if _, err := ParseTestJSON([]byte(input)); err == nil {
			t.Fatalf("accepted incomplete or invalid stream %q", input)
		}
	}
	res, err := ParseTestJSON([]byte(`{"Action":"pass","Package":"p"}`))
	if err != nil || !res["p"].Skipped {
		t.Fatalf("zero-test package counted as tested: %+v, %v", res, err)
	}
}

func TestRunFullSuiteArtifacts(t *testing.T) {
	dir, _ := testutil.FixtureRepo(t)
	logs := filepath.Join(t.TempDir(), "head")
	pkgs := []string{"example.com/fixture/core"}
	results, err := runFullSuite(context.Background(), dir, pkgs, time.Minute, nil, logs)
	if err != nil || !results[pkgs[0]].Passed || results[pkgs[0]].Skipped {
		t.Fatalf("results=%+v, error=%v", results, err)
	}
	raw, err := os.ReadFile(filepath.Join(logs, "stdout.jsonl"))
	if err != nil || !bytes.Contains(raw, []byte(`"Action":"pass"`)) {
		t.Fatalf("missing raw events: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logs, "stderr.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := runFullSuite(context.Background(), dir, pkgs, time.Minute, nil, logs); err == nil {
		t.Fatal("must not overwrite raw artifacts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunFullSuite(ctx, dir, pkgs, time.Minute, nil); err == nil {
		t.Fatal("canceled execution must not produce results")
	}
	if _, err := RunFullSuite(context.Background(), dir, nil, time.Minute, nil); err == nil {
		t.Fatal("empty universe must not run default package")
	}
}

func TestSummarizeAndCompute(t *testing.T) {
	res, err := ParseTestJSON([]byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	full := SummarizeFullSuite(res)
	if full.Packages != 2 || len(full.Failed) != 1 {
		t.Fatalf("full: %+v", full)
	}
	sel := Selection{Strategy: "static", Selected: []string{"example.com/b"}, TargetsTotal: 2}
	m := Compute(sel, full, HeadOracle(full), res)
	if m.Selected != 1 || m.ReductionPercent != 50 {
		t.Fatalf("m: %+v", m)
	}
	if m.Recall == nil || *m.Recall != 1 {
		t.Fatalf("recall: %+v", m.Recall)
	}
}

func TestWriteMarkdown(t *testing.T) {
	recs := []Record{
		{PR: 1, Strategies: []StrategyMetrics{
			{Strategy: "full", Selected: 4, TargetsTotal: 4},
			{Strategy: "jevci", Selected: 1, TargetsTotal: 4, ReductionPercent: 75, JevCostUSD: 0.001, FailedDetected: []string{}, FailedMissed: []string{}},
		}},
		{PR: 2, Strategies: []StrategyMetrics{
			{Strategy: "full", Selected: 4, TargetsTotal: 4},
			{Strategy: "jevci", Selected: 2, TargetsTotal: 4, ReductionPercent: 50, FailedDetected: []string{}, FailedMissed: []string{}},
		}},
	}
	var b bytes.Buffer
	WriteMarkdown(&b, recs)
	s := b.String()
	for _, want := range []string{"| strategy |", "jevci", "full", "selected/total", "n/a"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}
