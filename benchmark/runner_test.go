package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
	"github.com/abdelrahmanmagdii/jevci/internal/testutil"
)

func TestRevertSet(t *testing.T) {
	changes := []gitdiff.FileChange{
		{Path: "pkg/a.go", Status: gitdiff.Modified},
		{Path: "pkg/new.go", Status: gitdiff.Added},
		{Path: "pkg/a_test.go", Status: gitdiff.Modified},     // test: excluded
		{Path: "vendor/x/y.go", Status: gitdiff.Modified},     // vendor: excluded
		{Path: "pkg/testdata/z.go", Status: gitdiff.Modified}, // testdata: excluded
		{Path: "docs/readme.txt", Status: gitdiff.Modified},   // not .go: excluded
		{Path: "pkg/gone.go", Status: gitdiff.Deleted},
		{Path: "pkg/new_name.go", OldPath: "pkg/old_name.go", Status: gitdiff.Renamed},
	}
	restore, remove := revertSet(changes)
	wantRestore := []string{"pkg/a.go", "pkg/gone.go", "pkg/old_name.go"}
	wantRemove := []string{"pkg/new.go", "pkg/new_name.go"}
	if !reflect.DeepEqual(restore, wantRestore) {
		t.Fatalf("restore %v want %v", restore, wantRestore)
	}
	if !reflect.DeepEqual(remove, wantRemove) {
		t.Fatalf("remove %v want %v", remove, wantRemove)
	}
}

func TestLoadSuiteDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(p, []byte("repo: github.com/x/y\nworkdir: /tmp/w\nprs:\n  - number: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSuite(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Oracle != "head" || len(s.Packages) != 1 || s.Packages[0] != "./..." || s.TestTimeout == 0 {
		t.Fatalf("%+v", s)
	}
	if err := os.WriteFile(p, []byte("repo: x\nworkdir: w\noracle: bogus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(p); err == nil {
		t.Fatal("want oracle validation error")
	}
}

func TestParseStrategies(t *testing.T) {
	specs, err := parseStrategies([]string{"full", "jevci:no-direct"})
	if err != nil {
		t.Fatal(err)
	}
	if specs[1].label != "jevci:no-direct" || !specs[1].noDirect {
		t.Fatalf("%+v", specs[1])
	}
	if _, err := parseStrategies([]string{"nope"}); err == nil {
		t.Fatal("want error")
	}
}

func TestSourceReplayValidation(t *testing.T) {
	valid := Suite{Evaluation: "source_replay", SourceFiles: []string{"pkg/a.go"}, RunTests: true, Oracle: "revert", RequireHeadPass: true, ArtifactsDir: "artifacts", PRs: []PRSpec{{Base: "0000000000000000000000000000000000000001", Head: "0000000000000000000000000000000000000002"}}}
	if err := validateEvaluation(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Suite){
		func(s *Suite) { s.Evaluation = "" },
		func(s *Suite) { s.Evaluation = "unknown" },
		func(s *Suite) { s.SourceFiles = nil },
		func(s *Suite) { s.SourceFiles = []string{"pkg/a_test.go"} },
		func(s *Suite) { s.SourceFiles = []string{"go.mod"} },
		func(s *Suite) { s.SourceFiles = []string{"../a.go"} },
		func(s *Suite) { s.SourceFiles = []string{"vendor/a.go"} },
		func(s *Suite) { s.SourceFiles = []string{"pkg/testdata/a.go"} },
		func(s *Suite) { s.RunTests = false },
		func(s *Suite) { s.Oracle = "head" },
		func(s *Suite) { s.RequireHeadPass = false },
		func(s *Suite) { s.ArtifactsDir = "" },
		func(s *Suite) { s.PRs = []PRSpec{{Base: "HEAD~1", Head: "HEAD"}} },
	} {
		suite := valid
		mutate(&suite)
		if err := validateEvaluation(suite); err == nil {
			t.Fatalf("accepted invalid source replay: %+v", suite)
		}
	}
}

func TestSourceReplayKeepsTestsAndOtherSourceFixed(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	testutil.WriteFile(t, dir, "core/feature.go", "package core\nfunc ReplayValue() int { return 0 }\n")
	testutil.WriteFile(t, dir, "unrelated/feature.go", "package unrelated\nfunc ReplayOther() bool { return false }\n")
	base := commit("replay base")
	testutil.WriteFile(t, dir, "core/feature.go", "package core\nfunc ReplayValue() int { return 1 }\n")
	testutil.WriteFile(t, dir, "unrelated/feature.go", "package unrelated\nfunc ReplayOther() bool { return true }\n")
	testutil.WriteFile(t, dir, "api/replay_test.go", `package api
import (
 "testing"
 "example.com/fixture/core"
 "example.com/fixture/unrelated"
)
func TestReplayCore(t *testing.T) { if core.ReplayValue() != 1 { t.Fatal("core source reverted") } }
func TestReplayOther(t *testing.T) { if !unrelated.ReplayOther() { t.Fatal("other source must stay fixed") } }
`)
	head := commit("replay head")
	suite := Suite{Repo: "fixture", Workdir: dir, PRs: []PRSpec{{Number: 1, Base: base, Head: head}}, Strategies: []string{"changed", "static"}, RunTests: true, Packages: []string{"./api"}, Oracle: "revert", RequireHeadPass: true, TestTimeout: time.Minute, ArtifactsDir: t.TempDir(), Evaluation: "source_replay", SourceFiles: []string{"core/feature.go"}}
	var out bytes.Buffer
	records, err := Run(context.Background(), suite, config.Default(), nil, &out)
	if err != nil || len(records) != 1 || records[0].Error != "" {
		t.Fatalf("replay: %+v, %v", records, err)
	}
	r := records[0]
	if r.Evaluation != "source_replay" || !reflect.DeepEqual(r.SourceFiles, suite.SourceFiles) || !reflect.DeepEqual(r.Oracle.Failed, []string{"example.com/fixture/api"}) || r.Strategies[0].Selected != 0 || r.Strategies[1].Selected != 1 {
		t.Fatalf("wrong replay selection or oracle: %+v", r)
	}
	raw, err := os.ReadFile(filepath.Join(r.ArtifactsDir, "revert", "stdout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	results, err := ParseTestJSON(raw)
	if err != nil || !reflect.DeepEqual(results["example.com/fixture/api"].FailedTests, []string{"TestReplayCore"}) {
		t.Fatalf("tests or other source changed during replay: %+v, %v", results, err)
	}
	raw, err = os.ReadFile(filepath.Join(r.ArtifactsDir, "plans", "changed", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Evaluation string                          `json:"evaluation"`
		Plan       struct{ ChangedFiles []string } `json:"plan"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil || plan.Evaluation != "source_replay" || !reflect.DeepEqual(plan.Plan.ChangedFiles, suite.SourceFiles) {
		t.Fatalf("plan provenance: %+v, %v", plan, err)
	}
	if err := cleanCalibrationCheckout(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}

func TestRunRequiresPassingHead(t *testing.T) {
	dir, commit := testutil.FixtureRepo(t)
	base := commit("base")
	testutil.WriteFile(t, dir, "core/failing_test.go", "package core\nimport \"testing\"\nfunc TestFail(t *testing.T) { t.Fatal(\"head failure\") }\n")
	head := commit("failing head")
	suite := Suite{
		Repo: "fixture", Workdir: dir, PRs: []PRSpec{{Number: 1, Base: base, Head: head}},
		Strategies: []string{"full"}, RunTests: true, Packages: []string{"./core"},
		Oracle: "revert", RequireHeadPass: true, TestTimeout: time.Minute, ArtifactsDir: t.TempDir(),
	}
	var out bytes.Buffer
	records, err := Run(context.Background(), suite, config.Default(), nil, &out)
	if err != nil || len(records) != 1 || records[0].Error == "" || records[0].Oracle.Ran {
		t.Fatalf("head failure must stop oracle: %+v, %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(records[0].ArtifactsDir, "head", "stdout.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(records[0].ArtifactsDir, "revert")); !os.IsNotExist(err) {
		t.Fatalf("oracle artifacts created after head failure: %v", err)
	}
}

type calibrationScorer struct {
	results []semantic.Result
	err     error
}

func (s calibrationScorer) Name() string { return "calibration-test" }
func (s calibrationScorer) Score(context.Context, semantic.Change, []semantic.Candidate) ([]semantic.Result, error) {
	return s.results, s.err
}

func TestScoreCalibration(t *testing.T) {
	candidates := []semantic.Candidate{{ID: "p", ImportPath: "p"}, {ID: "q", ImportPath: "q"}}
	oracle := Oracle{Ran: true, Method: "revert", Failed: []string{"p", "outside"}}
	scorer := &semantic.MockScorer{Scores: map[string]float64{"p": 0.9, "q": 0}}
	r := scoreCalibration(context.Background(), PRSpec{Number: 1}, "repo", config.Default(), oracle, semantic.Change{Diff: "diff"}, candidates, scorer)
	if r.Error != "" || len(r.Candidates) != 2 || !r.Candidates[0].OraclePositive || r.Candidates[1].OraclePositive || !reflect.DeepEqual(r.UnscoredOraclePositives, []string{"outside"}) {
		t.Fatalf("%+v", r)
	}
	if r.Candidates[0].Probability == nil || *r.Candidates[0].Probability != 0.9 || r.Candidates[1].Probability == nil || *r.Candidates[1].Probability != 0 {
		t.Fatalf("scores %+v", r.Candidates)
	}
	for _, tc := range []struct {
		name   string
		scorer calibrationScorer
	}{
		{"short", calibrationScorer{}},
		{"long", calibrationScorer{results: make([]semantic.Result, 3)}},
		{"missing", calibrationScorer{results: make([]semantic.Result, 2)}},
		{"call error", calibrationScorer{err: errors.New("unavailable")}},
		{"candidate error", calibrationScorer{results: []semantic.Result{{Err: errors.New("unavailable")}, {Score: &semantic.Score{Probability: 0.7}}}}},
		{"invalid", calibrationScorer{results: []semantic.Result{{Score: &semantic.Score{Probability: -0.1}}, {Score: &semantic.Score{Probability: 1.1}}}}},
		{"nonfinite", calibrationScorer{results: []semantic.Result{{Score: &semantic.Score{Probability: math.NaN()}}, {Score: &semantic.Score{Probability: math.Inf(1)}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := scoreCalibration(context.Background(), PRSpec{}, "repo", config.Default(), oracle, semantic.Change{}, candidates, tc.scorer)
			if r.Error == "" || r.Candidates[0].Probability != nil || r.Candidates[0].Error == "" {
				t.Fatalf("errors must not become zero scores: %+v", r)
			}
			if _, err := json.Marshal(r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCalibrationOracles(t *testing.T) {
	pr := PRSpec{Number: 1, Base: "0000000000000000000000000000000000000001", Head: "0000000000000000000000000000000000000002"}
	suite := Suite{Repo: "repo", PRs: []PRSpec{pr}}
	valid := Record{Repo: "repo", PR: 1, Base: pr.Base, Head: pr.Head, FullSuite: FullSuite{Ran: true}, Oracle: Oracle{Ran: true, Method: "revert", Failed: []string{"p"}}}
	if _, err := calibrationOracles(suite, []Record{valid}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Record){
		func(r *Record) { r.Repo = "other" },
		func(r *Record) { r.Base = pr.Head },
		func(r *Record) { r.Head = pr.Base },
		func(r *Record) { r.Error = "failed" },
		func(r *Record) { r.FullSuite.Ran = false },
		func(r *Record) { r.Oracle.Ran = false },
		func(r *Record) { r.Oracle.Method = "head_failures" },
		func(r *Record) { r.Evaluation = "source_replay" },
		func(r *Record) { r.FullSuite.Failed = []string{"p"} },
		func(r *Record) { r.Oracle.Noise = []string{"p"} },
		func(r *Record) { r.Oracle.Failed = []string{"p", "p"} },
	} {
		r := valid
		mutate(&r)
		if _, err := calibrationOracles(suite, []Record{r}); err == nil {
			t.Fatalf("accepted invalid record %+v", r)
		}
	}
	if _, err := calibrationOracles(suite, []Record{valid, valid}); err == nil {
		t.Fatal("accepted duplicate oracle records")
	}
	suite.PRs = append(suite.PRs, pr)
	if _, err := calibrationOracles(suite, []Record{valid}); err == nil {
		t.Fatal("accepted duplicate suite PRs")
	}
	suite.PRs = nil
	if _, err := calibrationOracles(suite, nil); err == nil {
		t.Fatal("accepted empty suite")
	}
	suite.PRs = []PRSpec{{Number: 1, Base: "HEAD~1", Head: "HEAD"}}
	if _, err := calibrationOracles(suite, []Record{valid}); err == nil {
		t.Fatal("accepted unpinned commits")
	}
}

type calibrationFailWriter struct{}

func (calibrationFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestCalibrateRestoresCheckout(t *testing.T) {
	ctx := context.Background()
	dir, commit := testutil.FixtureRepo(t)
	base := commit("oracle base")
	testutil.WriteFile(t, dir, "core/extra.go", "package core\nfunc Extra() {}\n")
	head := commit("oracle head")
	original := commit("original checkout")
	branch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	pr := PRSpec{Number: 1, Base: base, Head: head}
	suite := Suite{Repo: "fixture", Workdir: dir, PRs: []PRSpec{pr}, Packages: []string{"./..."}}
	record := Record{Repo: "fixture", PR: 1, Base: base, Head: head, FullSuite: FullSuite{Ran: true}, Oracle: Oracle{Ran: true, Method: "revert", Failed: []string{"example.com/fixture/core"}}}
	mock := &semantic.MockScorer{Scores: map[string]float64{"example.com/fixture/core": 0.9}}
	factory := func() semantic.Scorer { return mock }
	var out bytes.Buffer
	if err := Calibrate(ctx, suite, config.Default(), []Record{record}, factory, &out); err != nil {
		t.Fatal(err)
	}
	var result CalibrationRecord
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || !result.Candidates[0].OraclePositive || result.Candidates[0].Probability == nil || *result.Candidates[0].Probability != 0.9 {
		t.Fatalf("%+v", result)
	}
	assertRestored := func() {
		t.Helper()
		r, err := gitdiff.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.ResolveRev("HEAD")
		if err != nil || got != original {
			t.Fatalf("restored head=%s error=%v", got, err)
		}
		gotBranch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil || !bytes.Equal(gotBranch, branch) {
			t.Fatalf("restored branch=%s error=%v", gotBranch, err)
		}
	}
	assertRestored()
	if err := Calibrate(ctx, suite, config.Default(), []Record{record}, factory, calibrationFailWriter{}); err == nil {
		t.Fatal("expected output error")
	}
	assertRestored()
	if err := Checkout(ctx, dir, original); err != nil {
		t.Fatal(err)
	}
	branch = []byte("HEAD\n")
	mock.Err = errors.New("unavailable")
	out.Reset()
	if err := Calibrate(ctx, suite, config.Default(), []Record{record}, factory, &out); err == nil {
		t.Fatal("expected scoring error")
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Error == "" || result.Candidates[0].Probability != nil {
		t.Fatalf("error record: %+v, %v", result, err)
	}
	assertRestored()
	testutil.WriteFile(t, dir, "local.txt", "preserve me\n")
	calls := mock.Calls
	if err := Calibrate(ctx, suite, config.Default(), []Record{record}, factory, &out); err == nil || mock.Calls != calls {
		t.Fatal("dirty checkout must prevent scoring")
	}
	data, err := os.ReadFile(filepath.Join(dir, "local.txt"))
	if err != nil || string(data) != "preserve me\n" {
		t.Fatalf("local file changed: %q, %v", data, err)
	}
	assertRestored()
}
