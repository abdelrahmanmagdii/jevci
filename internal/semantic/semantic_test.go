package semantic

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMockScorer(t *testing.T) {
	m := &MockScorer{Scores: map[string]float64{"a": 0.9}, PerCandidateErr: map[string]error{"b": errors.New("x")}}
	res, err := m.Score(context.Background(), Change{}, []Candidate{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Score.Probability != 0.9 || res[1].Err == nil || res[2].Score.Probability != 0 {
		t.Fatalf("%+v", res)
	}
	if m.Calls != 1 {
		t.Fatal("calls")
	}
	m.Err = errors.New("down")
	if _, err := m.Score(context.Background(), Change{}, nil); err == nil {
		t.Fatal("want err")
	}
}

func TestFallbackScorer(t *testing.T) {
	f := &FallbackScorer{Err: errors.New("no key")}
	res, err := f.Score(context.Background(), Change{}, []Candidate{{ID: "a"}})
	if err != nil || res[0].Err == nil {
		t.Fatal("want per-candidate err")
	}
}

func TestUsage(t *testing.T) {
	var u Usage
	u.Add(100, "m1", time.Second)
	u.Add(50, "m1", time.Second)
	s := u.Snapshot()
	if s.Requests != 2 || s.InputTokens != 150 || s.Latency != 2*time.Second {
		t.Fatalf("%d %d %v", s.Requests, s.InputTokens, s.Latency)
	}
}
