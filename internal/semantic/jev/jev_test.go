package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

func testCfg(url string) config.Jev {
	c := config.Default().Jev
	c.BaseURL = url
	c.BatchSize = 2
	c.Concurrency = 1
	c.MaxRetries = 3
	c.Timeout = 5 * time.Second
	return c
}

func cands(n int) []semantic.Candidate {
	out := make([]semantic.Candidate, n)
	for i := range out {
		out[i] = semantic.Candidate{ID: fmt.Sprintf("c%d", i), Package: fmt.Sprintf("./pkg/c%d", i)}
	}
	return out
}

func change() semantic.Change {
	return semantic.Change{ChangedPackages: []string{"./core"}, Diff: "@@ diff"}
}

func TestHappyPathTwoBatches(t *testing.T) {
	var calls int32
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("bad path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth")
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		bodies = append(bodies, req)
		n := atomic.AddInt32(&calls, 1)
		var answers map[string]any
		_ = n
		// Answer each question in this request with 0.5 + index.
		answers = map[string]any{}
		qs := req["questions"].(map[string]any)
		i := 0
		for id := range qs {
			answers[id] = map[string]any{"type": "noul", "noul": 0.5 + float64(i)*0.1}
			i++
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "jev-1.13.0",
			"answers": answers,
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 10},
		})
	}))
	defer srv.Close()

	u := &semantic.Usage{}
	s := New(testCfg(srv.URL), "k", u)
	res, err := s.Score(context.Background(), change(), cands(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 4 {
		t.Fatalf("got %d results", len(res))
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2", calls)
	}
	for _, r := range res {
		if r.Score == nil {
			t.Fatalf("missing score: %+v", r)
		}
	}
	// Question ids are per-batch local.
	for _, b := range bodies {
		qs := b["questions"].(map[string]any)
		if _, ok := qs["candidate_0"]; !ok {
			t.Fatal("missing candidate_0")
		}
		st := b["state"].(map[string]any)
		if st["change"].(map[string]any)["diff"] != "@@ diff" {
			t.Fatal("bad state")
		}
	}
	if u.Snapshot().Requests != 2 || u.Snapshot().InputTokens != 200 {
		t.Fatalf("usage %+v", u.Snapshot())
	}
}

func TestRetryOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "m",
			"answers": map[string]any{"candidate_0": map[string]any{"type": "noul", "noul": 0.9}},
		})
	}))
	defer srv.Close()
	s := New(testCfg(srv.URL), "k", nil)
	res, err := s.Score(context.Background(), change(), cands(1))
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Score == nil || res[0].Score.Probability != 0.9 {
		t.Fatalf("%+v", res[0])
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func Test401NoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	s := New(testCfg(srv.URL), "bad", nil)
	res, err := s.Score(context.Background(), change(), cands(1))
	if err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
	if res == nil || res[0].Err == nil {
		t.Fatalf("per-candidate err missing: %+v", res)
	}
}

func Test422Body(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		fmt.Fprint(w, `{"error":"bad question"}`)
	}))
	defer srv.Close()
	s := New(testCfg(srv.URL), "k", nil)
	res, err := s.Score(context.Background(), change(), cands(1))
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "bad question") {
		t.Fatalf("err=%v", res[0].Err)
	}
}

func TestMalformedAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "m",
			"answers": map[string]any{"candidate_0": map[string]any{"type": "noul"}},
		})
	}))
	defer srv.Close()
	s := New(testCfg(srv.URL), "k", nil)
	res, err := s.Score(context.Background(), change(), cands(1))
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil {
		t.Fatal("want per-candidate err")
	}
}

func TestContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := New(testCfg(srv.URL), "k", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := s.Score(ctx, change(), cands(1))
	_ = err
	if res[0].Err == nil {
		t.Fatal("want ctx err on candidate")
	}
}

func TestOversizeStateSplitsBatch(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"model": "m",
			"answers": map[string]any{
				"candidate_0": map[string]any{"type": "noul", "noul": 0.5},
			},
		})
	}))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	cfg.BatchSize = 4
	cfg.MaxStateChars = 200
	cfg.MaxDiffChars = 0
	s := New(cfg, "k", nil)
	ch := change()
	ch.Diff = strings.Repeat("x", 5000)
	res, err := s.Score(context.Background(), ch, cands(4))
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("calls=%d want >1 (batch split)", calls)
	}
	for i, r := range res {
		if r.Score == nil && r.Err == nil {
			t.Fatalf("candidate %d unresolved", i)
		}
	}
}

func TestLiveJev(t *testing.T) {
	if os.Getenv("JEVCI_LIVE_TEST") != "1" || os.Getenv("TYPESAFE_API_KEY") == "" {
		t.Skip("set JEVCI_LIVE_TEST=1 and TYPESAFE_API_KEY")
	}
	cfg := config.Default().Jev
	s := New(cfg, os.Getenv("TYPESAFE_API_KEY"), nil)
	res, err := s.Score(context.Background(), change(), cands(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res[0])
}
