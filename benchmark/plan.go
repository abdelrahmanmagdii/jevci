package benchmark

import (
	"context"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/planner"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

// PlanSelection runs one strategy and converts the plan to a Selection.
// Planner errors are recorded in Selection.Error, never panicked on.
func PlanSelection(ctx context.Context, repoDir, base, head string, strategy policy.Strategy, cfg config.Config, scorer semantic.Scorer) (Selection, error) {
	sel := Selection{Strategy: strategy.String()}
	start := time.Now()
	p, err := planner.Run(ctx, planner.Options{
		RepoDir: repoDir, Base: base, Head: head,
		Strategy: strategy, Config: cfg, Scorer: scorer,
	})
	sel.PlanMS = time.Since(start).Milliseconds()
	if err != nil {
		sel.Error = err.Error()
		return sel, nil
	}
	// Convert display paths back to import paths: selected targets keep
	// their ImportPath on the plan targets.
	impByRel := map[string]string{}
	for _, tg := range p.Targets {
		impByRel[tg.Package] = tg.ImportPath
	}
	for _, rel := range p.Selected {
		sel.Selected = append(sel.Selected, impByRel[rel])
	}
	sel.TargetsTotal = p.TargetsTotal
	sel.JevLatencyMS = p.Jev.LatencyMS
	sel.JevInputTokens = p.Jev.InputTokens
	sel.JevRequests = p.Jev.Requests
	sel.JevModel = p.Jev.Model
	if p.Jev.Error != "" && sel.Error == "" {
		sel.Error = "jev: " + p.Jev.Error
	}
	return sel, nil
}
