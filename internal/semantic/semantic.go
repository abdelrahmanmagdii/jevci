// Package semantic defines the scorer interface Jev implements.
package semantic

import (
	"context"
	"sync"
	"time"
)

// PricePerMillionInputTokensUSD is Jev's published input price
// (docs.typesafe.ai/models, jev-1.13.0, 2026-09). Output tokens are free.
const PricePerMillionInputTokensUSD = 0.042

// Candidate is a test package Jev may score.
type Candidate struct {
	ID           string
	Package      string // display path ./pkg/x
	ImportPath   string
	Relationship string
	TestFiles    []string
	Tests        []string // capped at 40, then "... and N more"
	PackageDoc   string
}

// Change describes the base..head diff for scoring.
type Change struct {
	Base, Head      string
	ChangedPackages []string
	ChangedFiles    []string
	ChangedSymbols  []string
	Diff            string
}

// Score is one noul answer.
type Score struct {
	Probability float64
	Model       string
	InputTokens int
}

// Result pairs a score with an optional per-candidate error.
type Result struct {
	Score *Score
	Err   error
}

// Scorer scores candidates against a change. It returns one Result per
// candidate in order. A per-candidate Err means fail-open for that
// candidate; a returned error means the whole call failed.
type Scorer interface {
	Score(ctx context.Context, change Change, candidates []Candidate) ([]Result, error)
	Name() string
}

// Usage accumulates Jev request accounting.
type Usage struct {
	mu          sync.Mutex
	Requests    int
	InputTokens int
	Model       string
	Latency     time.Duration
}

// Add records one request.
func (u *Usage) Add(inputTokens int, model string, d time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Requests++
	u.InputTokens += inputTokens
	if model != "" {
		u.Model = model
	}
	u.Latency += d
}

// Snapshot returns a consistent copy.
func (u *Usage) Snapshot() Usage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return Usage{Requests: u.Requests, InputTokens: u.InputTokens, Model: u.Model, Latency: u.Latency}
}

// MockScorer returns canned results for tests.
type MockScorer struct {
	Scores          map[string]float64 // by candidate ID
	Err             error              // whole-call error
	PerCandidateErr map[string]error
	Calls           int
}

// FallbackScorer reports err for every candidate; used when the real
// scorer cannot be constructed (e.g. missing API key).
type FallbackScorer struct{ Err error }

// Name implements Scorer.
func (f *FallbackScorer) Name() string { return "fallback" }

// Score implements Scorer.
func (f *FallbackScorer) Score(ctx context.Context, change Change, candidates []Candidate) ([]Result, error) {
	out := make([]Result, len(candidates))
	for i := range out {
		out[i].Err = f.Err
	}
	return out, nil
}

// Name implements Scorer.
func (m *MockScorer) Name() string { return "mock" }

// Score implements Scorer.
func (m *MockScorer) Score(ctx context.Context, change Change, candidates []Candidate) ([]Result, error) {
	m.Calls++
	if m.Err != nil {
		return nil, m.Err
	}
	out := make([]Result, len(candidates))
	for i, c := range candidates {
		if err := m.PerCandidateErr[c.ID]; err != nil {
			out[i].Err = err
			continue
		}
		p := m.Scores[c.ID]
		out[i].Score = &Score{Probability: p, Model: "mock"}
	}
	return out, nil
}
