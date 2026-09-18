// Package jev implements semantic.Scorer against TypeSafe's System One API.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

const path = "/v1/systemone"

type request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type response struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type stateCandidate struct {
	Index        int      `json:"index"`
	Package      string   `json:"package"`
	Relationship string   `json:"relationship"`
	TestFiles    []string `json:"test_files"`
	Tests        []string `json:"tests"`
	PackageDoc   string   `json:"package_doc,omitempty"`
}

type state struct {
	Change struct {
		ChangedPackages []string `json:"changed_packages"`
		ChangedFiles    []string `json:"changed_files"`
		ChangedSymbols  []string `json:"changed_symbols"`
		Diff            string   `json:"diff"`
	} `json:"change"`
	Candidates []stateCandidate `json:"candidates"`
}

// Scorer calls the System One API in batches.
type Scorer struct {
	cfg    config.Jev
	apiKey string
	hc     *http.Client
	usage  *semantic.Usage
}

// New returns a Scorer; apiKey may be empty (calls will 401).
func New(cfg config.Jev, apiKey string, usage *semantic.Usage) *Scorer {
	if usage == nil {
		usage = &semantic.Usage{}
	}
	return &Scorer{cfg: cfg, apiKey: apiKey, hc: &http.Client{Timeout: cfg.Timeout}, usage: usage}
}

// Usage returns the accumulated request accounting.
func (s *Scorer) Usage() semantic.Usage { return s.usage.Snapshot() }

// Name implements semantic.Scorer.
func (s *Scorer) Name() string { return "jev" }

type batchResult struct {
	idx []int // global candidate indices in this batch
	res []semantic.Result
	err error
}

// Score implements semantic.Scorer.
func (s *Scorer) Score(ctx context.Context, change semantic.Change, candidates []semantic.Candidate) ([]semantic.Result, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	batchSize := s.cfg.BatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	sem := make(chan struct{}, max(1, s.cfg.Concurrency))
	results := make([]semantic.Result, len(candidates))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	var fatal error
	for start := 0; start < len(candidates); start += batchSize {
		end := min(start+batchSize, len(candidates))
		idxs := make([]int, end-start)
		for i := range idxs {
			idxs[i] = start + i
		}
		wg.Add(1)
		go func(idxs []int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				for _, i := range idxs {
					results[i].Err = ctx.Err()
				}
				mu.Unlock()
				return
			}
			res, err := s.scoreBatch(ctx, change, candidates, idxs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				var ne *nonRetryable
				if errors.As(err, &ne) && ne.status == 401 {
					fatal = err
				}
				for _, i := range idxs {
					results[i].Err = err
				}
				return
			}
			for j, i := range idxs {
				results[i] = res[j]
			}
		}(idxs)
	}
	wg.Wait()
	if fatal != nil && len(errs)*batchSize >= len(candidates) {
		// All batches failed with 401: surface once so the planner can report
		// "Jev unavailable".
		return results, fatal
	}
	return results, nil
}

type nonRetryable struct {
	status int
	body   string
}

func (e *nonRetryable) Error() string {
	return fmt.Sprintf("jev: http %d: %s", e.status, e.body)
}

func (s *Scorer) scoreBatch(ctx context.Context, change semantic.Change, all []semantic.Candidate, idxs []int) ([]semantic.Result, error) {
	// Build state; if it exceeds max_state_chars drop the diff first, then
	// split the batch in half.
	diff := truncateDiff(change.Diff, s.cfg.MaxDiffChars)
	dropped := false
	for {
		body := s.buildRequest(change, all, idxs, diff)
		raw, _ := json.Marshal(body.State)
		if s.cfg.MaxStateChars <= 0 || len(raw) <= s.cfg.MaxStateChars {
			return s.do(ctx, body, idxs)
		}
		if !dropped {
			dropped = true
			if diff != "" {
				diff = "(omitted: too large)"
			}
			continue
		}
		if len(idxs) > 1 {
			half := len(idxs) / 2
			r1, err1 := s.scoreBatch(ctx, change, all, idxs[:half])
			r2, err2 := s.scoreBatch(ctx, change, all, idxs[half:])
			out := make([]semantic.Result, len(idxs))
			copy(out, r1)
			copy(out[half:], r2)
			switch {
			case err1 != nil && err2 != nil:
				return out, err1
			case err1 != nil:
				for i := range idxs[:half] {
					out[i].Err = err1
				}
			case err2 != nil:
				for i := half; i < len(idxs); i++ {
					out[i].Err = err2
				}
			}
			return out, nil
		}
		// Single candidate still oversized: send it anyway.
		return s.do(ctx, body, idxs)
	}
}

func (s *Scorer) buildRequest(change semantic.Change, all []semantic.Candidate, idxs []int, diff string) request {
	st := state{}
	st.Change.ChangedPackages = change.ChangedPackages
	st.Change.ChangedFiles = change.ChangedFiles
	st.Change.ChangedSymbols = change.ChangedSymbols
	st.Change.Diff = diff
	qs := map[string]question{}
	for j, gi := range idxs {
		c := all[gi]
		st.Candidates = append(st.Candidates, stateCandidate{
			Index: j, Package: c.Package, Relationship: c.Relationship,
			TestFiles: c.TestFiles, Tests: c.Tests, PackageDoc: c.PackageDoc,
		})
		id := fmt.Sprintf("candidate_%d", j)
		qs[id] = question{
			Type: "noul",
			Instructions: fmt.Sprintf(
				"Could the code change described in change.diff (changed packages: %s) alter the behavior that the tests in candidates[%d] (package %s) verify? Answer yes if a test in candidates[%d] could start failing or change outcome because of this change.",
				strings.Join(change.ChangedPackages, ", "), j, c.Package, j),
			Criteria: map[string]string{
				"true":  fmt.Sprintf("The tests in candidates[%d] exercise code paths, data, or contracts that the change modifies, directly or through the listed import relationship.", j),
				"false": fmt.Sprintf("The tests in candidates[%d] verify behavior that the change does not touch; they would pass or fail identically with or without the change.", j),
			},
		}
	}
	return request{Model: s.cfg.Model, State: st, Questions: qs}
}

func truncateDiff(d string, max int) string {
	if max > 0 && len(d) > max {
		return d[:max] + "\n... [diff truncated]"
	}
	return d
}

// do performs one request with retries; returns per-candidate results.
func (s *Scorer) do(ctx context.Context, req request, idxs []int) ([]semantic.Result, error) {
	if len(idxs) == 0 {
		return nil, fmt.Errorf("jev: empty batch")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := backoff(attempt, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		start := time.Now()
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.BaseURL, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := s.hc.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("jev: %w", err)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("jev: read body: %w", err)
			continue
		}
		lat := time.Since(start)
		switch {
		case resp.StatusCode == 200:
			var r response
			if err := json.Unmarshal(body, &r); err != nil {
				return nil, fmt.Errorf("jev: decode response: %w", err)
			}
			s.usage.Add(r.Usage.InputTokens, r.Model, lat)
			out := make([]semantic.Result, len(idxs))
			for j := range idxs {
				id := fmt.Sprintf("candidate_%d", j)
				ans, ok := r.Answers[id]
				if !ok || ans.Noul == nil {
					out[j].Err = fmt.Errorf("jev: missing noul answer for %s", id)
					continue
				}
				out[j].Score = &semantic.Score{Probability: *ans.Noul, Model: r.Model, InputTokens: r.Usage.InputTokens}
			}
			return out, nil
		case resp.StatusCode == 401 || resp.StatusCode == 422:
			return nil, &nonRetryable{status: resp.StatusCode, body: string(body)}
		case resp.StatusCode == 429 || resp.StatusCode == 529 || resp.StatusCode >= 500:
			lastErr = &retryableHTTP{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After"), body: string(body)}
			continue
		default:
			return nil, &nonRetryable{status: resp.StatusCode, body: string(body)}
		}
	}
	return nil, fmt.Errorf("jev: retries exhausted: %w", lastErr)
}

type retryableHTTP struct {
	status     int
	retryAfter string
	body       string
}

func (e *retryableHTTP) Error() string { return fmt.Sprintf("jev: http %d: %s", e.status, e.body) }

func backoff(attempt int, lastErr error) time.Duration {
	if re, ok := lastErr.(*retryableHTTP); ok && re.retryAfter != "" {
		if secs, err := strconv.Atoi(re.retryAfter); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(math.Pow(2, float64(attempt))) * 100 * time.Millisecond
}
