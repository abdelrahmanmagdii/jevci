package benchmark

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/planner"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

type CalibrationCandidate struct {
	Input          semantic.Candidate `json:"input"`
	OraclePositive bool               `json:"oracle_positive"`
	Probability    *float64           `json:"probability"`
	Model          string             `json:"model,omitempty"`
	Error          string             `json:"error,omitempty"`
}

type CalibrationRecord struct {
	Version                 int                    `json:"version"`
	Repo                    string                 `json:"repo"`
	PR                      int                    `json:"pr"`
	Base                    string                 `json:"base"`
	Head                    string                 `json:"head"`
	CreatedAt               time.Time              `json:"created_at"`
	Config                  config.Config          `json:"config"`
	Change                  semantic.Change        `json:"change"`
	Oracle                  Oracle                 `json:"oracle"`
	Candidates              []CalibrationCandidate `json:"candidates"`
	UnscoredOraclePositives []string               `json:"unscored_oracle_positives"`
	Requests                int                    `json:"requests"`
	InputTokens             int                    `json:"input_tokens"`
	CostUSD                 float64                `json:"cost_usd"`
	ScoringMS               int64                  `json:"scoring_ms"`
	Error                   string                 `json:"error,omitempty"`
}

func calibrationOracles(suite Suite, records []Record) (map[int]Record, error) {
	if len(suite.PRs) == 0 {
		return nil, fmt.Errorf("calibration requires at least one PR")
	}
	byPR := map[int]Record{}
	for _, r := range records {
		if r.Repo != suite.Repo {
			continue
		}
		if _, exists := byPR[r.PR]; exists {
			return nil, fmt.Errorf("duplicate oracle record for PR %d", r.PR)
		}
		byPR[r.PR] = r
	}
	seen := map[int]bool{}
	for _, pr := range suite.PRs {
		if seen[pr.Number] {
			return nil, fmt.Errorf("duplicate suite PR %d", pr.Number)
		}
		seen[pr.Number] = true
		for _, rev := range []string{pr.Base, pr.Head} {
			if b, err := hex.DecodeString(rev); err != nil || len(b) != 20 {
				return nil, fmt.Errorf("PR %d: calibration requires full base and head commit SHAs", pr.Number)
			}
		}
		r, ok := byPR[pr.Number]
		if !ok || r.Base != pr.Base || r.Head != pr.Head {
			return nil, fmt.Errorf("PR %d: no oracle record matches repository, base and head", pr.Number)
		}
		if r.Error != "" || !r.FullSuite.Ran || !r.Oracle.Ran || r.Oracle.Method != "revert" || evaluationName(r.Evaluation) != "historical_pr" {
			return nil, fmt.Errorf("PR %d: calibration requires an error-free measured revert oracle", pr.Number)
		}
		failedAtHead := map[string]bool{}
		for _, ip := range append(append([]string{}, r.FullSuite.Failed...), r.Oracle.Noise...) {
			failedAtHead[ip] = true
		}
		positives := map[string]bool{}
		for _, ip := range r.Oracle.Failed {
			if ip == "" || positives[ip] || failedAtHead[ip] {
				return nil, fmt.Errorf("PR %d: invalid or duplicate oracle positive %q", pr.Number, ip)
			}
			positives[ip] = true
		}
	}
	return byPR, nil
}

func cleanCalibrationCheckout(ctx context.Context, dir string) error {
	status, err := git(ctx, dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return fmt.Errorf("calibration requires a clean checkout; local changes are preserved")
	}
	return nil
}

func Calibrate(ctx context.Context, suite Suite, cfg config.Config, records []Record, factory ScorerFactory, out io.Writer) (err error) {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !cfg.Jev.Enabled || factory == nil {
		return fmt.Errorf("calibration requires an enabled scorer; fail-open decisions are not scores")
	}
	oracles, err := calibrationOracles(suite, records)
	if err != nil {
		return err
	}
	var exclude *regexp.Regexp
	if suite.ExcludePackageRegex != "" {
		exclude, err = regexp.Compile(suite.ExcludePackageRegex)
		if err != nil {
			return fmt.Errorf("exclude_package_regex: %w", err)
		}
	}
	if err := cleanCalibrationCheckout(ctx, suite.Workdir); err != nil {
		return err
	}
	original, err := git(ctx, suite.Workdir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	branch, err := git(ctx, suite.Workdir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		restoreErr := cleanCalibrationCheckout(restoreCtx, suite.Workdir)
		if restoreErr == nil {
			if ref := strings.TrimSpace(string(branch)); ref != "HEAD" {
				_, restoreErr = git(restoreCtx, suite.Workdir, "checkout", "-q", ref)
			} else {
				restoreErr = Checkout(restoreCtx, suite.Workdir, strings.TrimSpace(string(original)))
			}
		}
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore original checkout: %w", restoreErr))
		}
	}()
	enc := json.NewEncoder(out)
	for _, pr := range suite.PRs {
		if err := cleanCalibrationCheckout(ctx, suite.Workdir); err != nil {
			return err
		}
		if err := Checkout(ctx, suite.Workdir, pr.Head); err != nil {
			return err
		}
		universe, err := resolvePackages(ctx, suite.Workdir, suite.Packages, exclude)
		if err != nil {
			return err
		}
		change, candidates, err := planner.CalibrationInput(ctx, planner.Options{
			RepoDir: suite.Workdir, Base: pr.Base, Head: pr.Head, Config: cfg,
		}, universe)
		if err != nil {
			return fmt.Errorf("PR %d: calibration input: %w", pr.Number, err)
		}
		if change.Base != pr.Base || change.Head != pr.Head {
			return fmt.Errorf("PR %d: planner revisions do not match oracle revisions", pr.Number)
		}
		scorer := factory()
		if scorer == nil {
			return fmt.Errorf("calibration scorer factory returned nil")
		}
		rec := scoreCalibration(ctx, pr, suite.Repo, cfg, oracles[pr.Number].Oracle, change, candidates, scorer)
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("write calibration record: %w", err)
		}
		log.Printf("PR %d: calibrated %d changed packages, %d oracle positives outside candidates", pr.Number, len(candidates), len(rec.UnscoredOraclePositives))
		if rec.Error != "" {
			return fmt.Errorf("PR %d: %s", pr.Number, rec.Error)
		}
	}
	return nil
}

func scoreCalibration(ctx context.Context, pr PRSpec, repo string, cfg config.Config, oracle Oracle, change semantic.Change, candidates []semantic.Candidate, scorer semantic.Scorer) CalibrationRecord {
	rec := CalibrationRecord{
		Version: 1, Repo: repo, PR: pr.Number, Base: pr.Base, Head: pr.Head,
		CreatedAt: time.Now().UTC(), Config: cfg, Change: change, Oracle: oracle,
		Candidates: []CalibrationCandidate{}, UnscoredOraclePositives: []string{},
	}
	positive := map[string]bool{}
	for _, ip := range oracle.Failed {
		positive[ip] = true
	}
	start := time.Now()
	var results []semantic.Result
	var err error
	if len(candidates) > 0 {
		results, err = scorer.Score(ctx, change, candidates)
	}
	rec.ScoringMS = time.Since(start).Milliseconds()
	if err != nil {
		rec.Error = err.Error()
	} else if len(results) != len(candidates) {
		rec.Error = fmt.Sprintf("scorer returned %d results for %d candidates", len(results), len(candidates))
	}
	seen := map[string]bool{}
	for i, c := range candidates {
		seen[c.ImportPath] = true
		row := CalibrationCandidate{Input: c, OraclePositive: positive[c.ImportPath]}
		switch {
		case err != nil || len(results) != len(candidates):
			row.Error = rec.Error
		case results[i].Err != nil:
			row.Error = results[i].Err.Error()
		case results[i].Score == nil:
			row.Error = "scorer returned no score"
		default:
			score := results[i].Score
			v := score.Probability
			row.Model = score.Model
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
				row.Error = fmt.Sprintf("invalid probability %v", v)
			} else {
				row.Probability = &v
			}
		}
		if row.Error != "" && rec.Error == "" {
			rec.Error = "one or more candidates have scoring errors"
		}
		rec.Candidates = append(rec.Candidates, row)
	}
	for _, ip := range oracle.Failed {
		if !seen[ip] {
			rec.UnscoredOraclePositives = append(rec.UnscoredOraclePositives, ip)
		}
	}
	if u, ok := scorer.(interface{ Usage() semantic.Usage }); ok {
		usage := u.Usage()
		rec.Requests, rec.InputTokens = usage.Requests, usage.InputTokens
		rec.CostUSD = float64(usage.InputTokens) / 1e6 * semantic.PricePerMillionInputTokensUSD
	}
	return rec
}
