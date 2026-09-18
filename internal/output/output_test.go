package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/planner"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
)

func samplePlan() *planner.Plan {
	var p planner.Plan
	p.Base = "base1"
	p.Head = "head1"
	p.MergeBase = "mb1"
	p.Strategy = "jevci"
	p.ChangedPackages = []string{"./pkg/cache"}
	p.ChangedFiles = []string{"pkg/cache/cache.go"}
	p.Targets = []planner.Target{
		{Package: "./pkg/cache", Class: policy.ClassChangedPackage,
			Decision: policy.Decision{Action: policy.Run, Source: policy.SourceChanged, Relevance: 1, Reason: "changed"}},
		{Package: "./pkg/api", Class: policy.ClassDirectDependent, Distance: 1, Via: []string{"./pkg/cache"},
			Decision: policy.Decision{Action: policy.Run, Source: policy.SourceStatic, Relevance: 1, Reason: "imports changed"}},
		{Package: "./pkg/web", Class: policy.ClassTransitiveDependent, Distance: 2, Via: []string{"./pkg/cache", "./pkg/api"},
			Decision: policy.Decision{Action: policy.Run, Source: policy.SourceJev, Relevance: 0.83, Reason: "jev"}},
		{Package: "./pkg/x", Class: policy.ClassUnrelated,
			Decision: policy.Decision{Action: policy.Skip, Source: policy.SourceUnrelated, Relevance: 0}},
	}
	p.Selected = []string{"./pkg/cache", "./pkg/api", "./pkg/web"}
	p.Skipped = []string{"./pkg/x"}
	p.TargetsTotal = 4
	p.TargetsSelected = 3
	p.ReductionPercent = 25.0
	p.Jev.Enabled = true
	p.Jev.Model = "jev-1.13.0"
	p.Jev.Requests = 1
	p.Jev.InputTokens = 300
	p.Jev.Candidates = 1
	p.Jev.LatencyMS = 1400
	return &p
}

func TestHuman(t *testing.T) {
	var b bytes.Buffer
	Human(&b, samplePlan())
	s := b.String()
	for _, want := range []string{
		"JevCI", "Changed packages:", "./pkg/cache", "4 test targets discovered.",
		"TEST TARGET", "SOURCE", "RELEVANCE", "ACTION",
		"./pkg/web", "Jev", "83%", "RUN",
		"Selected: 3 / 4 targets", "Reduction: 25.0%", "Jev planning time: 1.4s",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestHumanHidesSkipsOver60(t *testing.T) {
	var p planner.Plan
	p.Strategy = "jevci"
	for i := 0; i < 70; i++ {
		tg := planner.Target{Package: strings.Repeat("x", 1) + "./p" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Decision: policy.Decision{Action: policy.Skip, Source: policy.SourceUnrelated}}
		p.Targets = append(p.Targets, tg)
	}
	p.Targets[0].Decision = policy.Decision{Action: policy.Run, Source: policy.SourceChanged, Relevance: 1}
	p.TargetsTotal = 70
	p.TargetsSelected = 1
	var b bytes.Buffer
	Human(&b, &p)
	s := b.String()
	if !strings.Contains(s, "(+ 69 skipped targets hidden; use --json for all)") {
		t.Fatalf("missing hidden line:\n%s", s)
	}
}

func TestJSON(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, samplePlan()); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"].(float64) != 1 {
		t.Error("version")
	}
	if doc["targets_selected"].(float64) != 3 {
		t.Error("targets_selected")
	}
	jev := doc["jev"].(map[string]any)
	if jev["model"] != "jev-1.13.0" || jev["input_tokens"].(float64) != 300 {
		t.Errorf("jev: %v", jev)
	}
	targets := doc["targets"].([]any)
	if len(targets) != 4 {
		t.Fatal("targets")
	}
	t0 := targets[0].(map[string]any)
	if t0["action"] != "RUN" || t0["source"] != "changed" || t0["class"] != "changed" {
		t.Errorf("target0: %v", t0)
	}
	// arrays serialize as [] not null
	var b2 bytes.Buffer
	if err := JSON(&b2, &planner.Plan{}); err != nil {
		t.Fatal(err)
	}
	var doc2 map[string]any
	json.Unmarshal(b2.Bytes(), &doc2)
	for _, k := range []string{"changed_files", "selected_packages", "targets"} {
		if doc2[k] == nil {
			t.Errorf("%s is null", k)
		}
	}
}

func TestPackages(t *testing.T) {
	var b bytes.Buffer
	Packages(&b, samplePlan())
	if b.String() != "./pkg/cache\n./pkg/api\n./pkg/web\n" {
		t.Fatalf("%q", b.String())
	}
}
