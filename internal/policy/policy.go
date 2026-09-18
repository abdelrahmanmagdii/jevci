// Package policy maps static classification plus optional Jev scores to a
// run decision. It performs no I/O.
package policy

import (
	"fmt"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
)

// Action is the decision for a test target.
type Action int

const (
	Run Action = iota
	RunPackage
	Skip
)

func (a Action) String() string {
	switch a {
	case Run:
		return "RUN"
	case RunPackage:
		return "RUN PACKAGE"
	case Skip:
		return "SKIP"
	}
	return "?"
}

// Source explains where a decision came from.
type Source string

const (
	SourceChanged      Source = "changed"
	SourceChangedTest  Source = "changed-test"
	SourceProtected    Source = "protected"
	SourceStatic       Source = "static"
	SourceModule       Source = "module"
	SourceUnattributed Source = "unattributed"
	SourceJev          Source = "jev"
	SourceFallback     Source = "fallback"
	SourceUnrelated    Source = "unrelated"
	SourceFull         Source = "full"
	SourceNested       Source = "nested"
)

// StaticClass is the deterministic classification of a test package.
type StaticClass int

const (
	ClassChangedPackage StaticClass = iota
	ClassChangedTest
	ClassProtected
	ClassDirectDependent
	ClassTransitiveDependent
	ClassUnrelated
)

func (c StaticClass) String() string {
	switch c {
	case ClassChangedPackage:
		return "changed"
	case ClassChangedTest:
		return "changed-test"
	case ClassProtected:
		return "protected"
	case ClassDirectDependent:
		return "direct"
	case ClassTransitiveDependent:
		return "transitive"
	case ClassUnrelated:
		return "unrelated"
	}
	return "?"
}

// Strategy selects how much of the pipeline runs.
type Strategy int

const (
	StrategyFull Strategy = iota
	StrategyChanged
	StrategyStatic
	StrategyJevCI
)

func (s Strategy) String() string {
	switch s {
	case StrategyFull:
		return "full"
	case StrategyChanged:
		return "changed"
	case StrategyStatic:
		return "static"
	case StrategyJevCI:
		return "jevci"
	}
	return "?"
}

// ParseStrategy converts a CLI/config string.
func ParseStrategy(s string) (Strategy, error) {
	switch s {
	case "full":
		return StrategyFull, nil
	case "changed":
		return StrategyChanged, nil
	case "static":
		return StrategyStatic, nil
	case "jevci":
		return StrategyJevCI, nil
	}
	return 0, fmt.Errorf("unknown strategy %q (want jevci|static|changed|full)", s)
}

// ReasonUnattributed is the RunAllReason for the unattributed-files trigger.
const ReasonUnattributed = "unattributed files changed"

// ReasonNestedModule is the RunAllReason for the nested-modules trigger.
const ReasonNestedModule = "nested module files changed"

// Input is everything Decide needs for one target.
type Input struct {
	Class        StaticClass
	Distance     int
	Score        *float64 // nil = Jev not consulted
	ScoreErr     error    // non-nil = Jev consulted and failed
	RunAll       bool
	RunAllReason string
	Strategy     Strategy
	ViaRoot      string // display path of the changed package reached, for reasons
}

// Decision is the outcome for one target.
type Decision struct {
	Action    Action
	Source    Source
	Relevance float64
	Reason    string
}

// Decide applies the ordered rules.
func Decide(cfg config.Safety, jev config.Jev, in Input) Decision {
	if in.RunAll {
		src := SourceModule
		switch in.RunAllReason {
		case ReasonUnattributed:
			src = SourceUnattributed
		case ReasonNestedModule:
			src = SourceNested
		}
		reason := in.RunAllReason
		if reason == "" {
			reason = "module-level change; running everything"
		}
		return Decision{Action: Run, Source: src, Relevance: 1, Reason: reason}
	}
	if in.Strategy == StrategyFull {
		return Decision{Action: Run, Source: SourceFull, Relevance: 1, Reason: "strategy full: run all test targets"}
	}
	switch in.Class {
	case ClassChangedPackage:
		if cfg.AlwaysRunChangedPackages {
			return Decision{Run, SourceChanged, 1, "package changed"}
		}
	case ClassChangedTest:
		if cfg.AlwaysRunChangedTests {
			return Decision{Run, SourceChangedTest, 1, "test file changed"}
		}
	case ClassProtected:
		return Decision{Run, SourceProtected, 1, "protected package"}
	}
	if in.Strategy == StrategyChanged {
		return Decision{Skip, SourceUnrelated, 0, "strategy changed: only changed packages run"}
	}
	if in.Class == ClassDirectDependent && cfg.AlwaysRunDirectDependents {
		reason := "directly imports changed package"
		if in.ViaRoot != "" {
			reason = "imports changed package " + in.ViaRoot
		}
		return Decision{Run, SourceStatic, 1, reason}
	}
	if in.Strategy == StrategyStatic {
		if in.Distance >= 1 || in.Class == ClassTransitiveDependent || in.Class == ClassDirectDependent {
			return Decision{Run, SourceStatic, 1, "depends on changed package"}
		}
		return Decision{Skip, SourceUnrelated, 0, "no dependency on changed packages"}
	}
	// StrategyJevCI.
	if in.Class == ClassUnrelated && in.Score == nil && in.ScoreErr == nil {
		return Decision{Skip, SourceUnrelated, 0, "no dependency on changed packages"}
	}
	if in.Class == ClassDirectDependent || in.Class == ClassTransitiveDependent || in.Class == ClassUnrelated {
		if in.ScoreErr != nil {
			if cfg.FailOpen {
				return Decision{RunPackage, SourceFallback, 1, fmt.Sprintf("Jev unavailable: %v; fail-open", in.ScoreErr)}
			}
			return Decision{Skip, SourceFallback, 0, fmt.Sprintf("Jev unavailable: %v; fail-closed", in.ScoreErr)}
		}
		if in.Score == nil {
			return Decision{RunPackage, SourceFallback, 1, "Jev not consulted; fail-open"}
		}
		s := *in.Score
		switch {
		case s >= jev.RunThreshold:
			return Decision{Run, SourceJev, s, fmt.Sprintf("Jev relevance %.2f ≥ %.2f", s, jev.RunThreshold)}
		case s >= jev.UncertainThreshold:
			return Decision{RunPackage, SourceJev, s, fmt.Sprintf("Jev relevance %.2f in [%.2f, %.2f)", s, jev.UncertainThreshold, jev.RunThreshold)}
		default:
			return Decision{Skip, SourceJev, s, fmt.Sprintf("Jev relevance %.2f < %.2f", s, jev.UncertainThreshold)}
		}
	}
	return Decision{Skip, SourceUnrelated, 0, "no dependency on changed packages"}
}
