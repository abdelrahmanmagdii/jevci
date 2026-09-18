package policy

import (
	"errors"
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
)

var defJ = config.Default().Jev
var defS = config.Default().Safety

func f(x float64) *float64 { return &x }

func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		safety     config.Safety
		in         Input
		wantAction Action
		wantSource Source
	}{
		{"runall wins", defS, Input{RunAll: true, Class: ClassUnrelated, Strategy: StrategyJevCI}, Run, SourceModule},
		{"runall unattributed", defS, Input{RunAll: true, RunAllReason: ReasonUnattributed, Class: ClassUnrelated, Strategy: StrategyJevCI}, Run, SourceUnattributed},
		{"full", defS, Input{Class: ClassUnrelated, Strategy: StrategyFull}, Run, SourceFull},
		{"changed", defS, Input{Class: ClassChangedPackage, Strategy: StrategyJevCI}, Run, SourceChanged},
		{"changed disabled", func() config.Safety { s := defS; s.AlwaysRunChangedPackages = false; return s }(),
			Input{Class: ClassChangedPackage, Strategy: StrategyChanged}, Skip, SourceUnrelated},
		{"changed test", defS, Input{Class: ClassChangedTest, Strategy: StrategyStatic}, Run, SourceChangedTest},
		{"protected", defS, Input{Class: ClassProtected, Strategy: StrategyChanged}, Run, SourceProtected},
		{"direct", defS, Input{Class: ClassDirectDependent, Distance: 1, Strategy: StrategyJevCI}, Run, SourceStatic},
		{"direct disabled no score", func() config.Safety { s := defS; s.AlwaysRunDirectDependents = false; return s }(),
			Input{Class: ClassDirectDependent, Distance: 1, Strategy: StrategyJevCI}, RunPackage, SourceFallback},
		{"direct disabled jev run", func() config.Safety { s := defS; s.AlwaysRunDirectDependents = false; return s }(),
			Input{Class: ClassDirectDependent, Distance: 1, Score: f(0.9), Strategy: StrategyJevCI}, Run, SourceJev},
		{"direct disabled jev skip", func() config.Safety { s := defS; s.AlwaysRunDirectDependents = false; return s }(),
			Input{Class: ClassDirectDependent, Distance: 1, Score: f(0.1), Strategy: StrategyJevCI}, Skip, SourceJev},
		{"direct disabled jev err", func() config.Safety { s := defS; s.AlwaysRunDirectDependents = false; return s }(),
			Input{Class: ClassDirectDependent, Distance: 1, ScoreErr: errors.New("x"), Strategy: StrategyJevCI}, RunPackage, SourceFallback},
		{"static direct disabled", func() config.Safety { s := defS; s.AlwaysRunDirectDependents = false; return s }(),
			Input{Class: ClassDirectDependent, Distance: 1, Strategy: StrategyStatic}, Run, SourceStatic},
		{"changed strategy skips direct", defS, Input{Class: ClassDirectDependent, Strategy: StrategyChanged}, Skip, SourceUnrelated},
		{"static transitive", defS, Input{Class: ClassTransitiveDependent, Distance: 3, Strategy: StrategyStatic}, Run, SourceStatic},
		{"static unrelated", defS, Input{Class: ClassUnrelated, Strategy: StrategyStatic}, Skip, SourceUnrelated},
		{"jev run", defS, Input{Class: ClassTransitiveDependent, Distance: 2, Score: f(0.9), Strategy: StrategyJevCI}, Run, SourceJev},
		{"jev boundary run", defS, Input{Class: ClassTransitiveDependent, Distance: 2, Score: f(0.70), Strategy: StrategyJevCI}, Run, SourceJev},
		{"jev uncertain", defS, Input{Class: ClassTransitiveDependent, Distance: 2, Score: f(0.5), Strategy: StrategyJevCI}, RunPackage, SourceJev},
		{"jev boundary uncertain", defS, Input{Class: ClassTransitiveDependent, Distance: 2, Score: f(0.30), Strategy: StrategyJevCI}, RunPackage, SourceJev},
		{"jev skip", defS, Input{Class: ClassTransitiveDependent, Distance: 2, Score: f(0.29), Strategy: StrategyJevCI}, Skip, SourceJev},
		{"jev err fail open", defS, Input{Class: ClassTransitiveDependent, Distance: 2, ScoreErr: errors.New("boom"), Strategy: StrategyJevCI}, RunPackage, SourceFallback},
		{"jev err fail closed", func() config.Safety { s := defS; s.FailOpen = false; return s }(),
			Input{Class: ClassTransitiveDependent, Distance: 2, ScoreErr: errors.New("boom"), Strategy: StrategyJevCI}, Skip, SourceFallback},
		{"jev no score fallback", defS, Input{Class: ClassTransitiveDependent, Distance: 2, Strategy: StrategyJevCI}, RunPackage, SourceFallback},
		{"jev unrelated scored", defS, Input{Class: ClassUnrelated, Score: f(0.9), Strategy: StrategyJevCI}, Run, SourceJev},
		{"jev unrelated no score", defS, Input{Class: ClassUnrelated, Strategy: StrategyJevCI}, Skip, SourceUnrelated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.safety, defJ, tc.in)
			if d.Action != tc.wantAction || d.Source != tc.wantSource {
				t.Fatalf("got %v/%v want %v/%v", d.Action, d.Source, tc.wantAction, tc.wantSource)
			}
		})
	}
}
