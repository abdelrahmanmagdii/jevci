package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/planner"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

// PlanSelection runs one strategy and converts the plan to a Selection.
// Planner errors are recorded in Selection.Error, never panicked on.
func PlanSelection(ctx context.Context, repoDir, base, head string, strategy policy.Strategy, cfg config.Config, scorer semantic.Scorer) (Selection, error) {
	return planSelection(ctx, repoDir, base, head, strategy, cfg, scorer, nil, "")
}

func planSelection(ctx context.Context, repoDir, base, head string, strategy policy.Strategy, cfg config.Config, scorer semantic.Scorer, sourceFiles []string, artifactDir string) (Selection, error) {
	sel := Selection{Strategy: strategy.String()}
	start := time.Now()
	p, err := planner.Run(ctx, planner.Options{
		RepoDir: repoDir, Base: base, Head: head,
		Strategy: strategy, Config: cfg, Scorer: scorer, SourceFiles: sourceFiles,
	})
	sel.PlanMS = time.Since(start).Milliseconds()
	if err != nil {
		sel.Error = err.Error()
		return sel, nil
	}
	if artifactDir != "" {
		if err := os.MkdirAll(artifactDir, 0o755); err != nil {
			return sel, err
		}
		f, err := os.OpenFile(filepath.Join(artifactDir, "plan.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return sel, err
		}
		evaluation := "historical_pr"
		if len(sourceFiles) > 0 {
			evaluation = "source_replay"
		}
		err = json.NewEncoder(f).Encode(struct {
			Evaluation  string        `json:"evaluation"`
			SourceFiles []string      `json:"source_files"`
			Config      config.Config `json:"config"`
			Plan        *planner.Plan `json:"plan"`
		}{evaluation, sourceFiles, cfg, p})
		if err := errors.Join(err, f.Close()); err != nil {
			return sel, err
		}
	}
	// Convert display paths back to import paths: selected targets keep
	// their ImportPath on the plan targets.
	impByRel := map[string]string{}
	for _, tg := range p.Targets {
		impByRel[tg.Package] = tg.ImportPath
		if tg.ScoreErr != "" && sel.Error == "" {
			sel.Error = "jev: " + tg.ScoreErr
		}
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
