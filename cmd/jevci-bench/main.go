// jevci-bench runs JevCI benchmark suites and renders reports.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdelrahmanmagdii/jevci/benchmark"
	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic/jev"
)

func main() { os.Exit(run(os.Args[1:])) }

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  jevci-bench run --suite <file.yaml> --out <results.jsonl> [--no-tests] [--strategies a,b] [--config .jevci.yaml]
  jevci-bench calibrate --suite <file.yaml> --oracle <results.jsonl> --out <scores.jsonl> [--config .jevci.yaml]
  jevci-bench report --in <results.jsonl> [--md out.md]
`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "run":
		return runBench(args[1:])
	case "report":
		return runReport(args[1:])
	case "calibrate":
		return runCalibration(args[1:])
	default:
		usage()
		return 2
	}
}

func runBench(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	suitePath := fs.String("suite", "", "suite YAML file (required)")
	outPath := fs.String("out", "", "results JSONL path (required)")
	noTests := fs.Bool("no-tests", false, "plan only; do not run go test or the oracle")
	strategies := fs.String("strategies", "", "comma-separated strategy override")
	limit := fs.Int("limit", 0, "only the first N PRs")
	only := fs.String("only", "", "comma-separated PR numbers to run")
	cfgPath := fs.String("config", "", "config file (default: suite config or .jevci.yaml)")
	timeout := fs.Duration("timeout", 24*time.Hour, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *suitePath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "error: --suite and --out are required")
		fs.Usage()
		return 2
	}
	suite, err := benchmark.LoadSuite(*suitePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *noTests {
		suite.RunTests = false
		suite.Oracle = "head"
	}
	if *strategies != "" {
		suite.Strategies = strings.Split(*strategies, ",")
	}
	if *only != "" {
		want := map[int]bool{}
		for _, s := range strings.Split(*only, ",") {
			var n int
			fmt.Sscanf(s, "%d", &n)
			want[n] = true
		}
		var prs []benchmark.PRSpec
		for _, p := range suite.PRs {
			if want[p.Number] {
				prs = append(prs, p)
			}
		}
		suite.PRs = prs
	}
	if *limit > 0 && len(suite.PRs) > *limit {
		suite.PRs = suite.PRs[:*limit]
	}
	cfgFile := *cfgPath
	if cfgFile == "" {
		cfgFile = suite.Config
	}
	if cfgFile == "" {
		cfgFile = ".jevci.yaml"
	}
	cfg, err := config.Load(cfgFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error: invalid config:", err)
		return 2
	}

	var factory benchmark.ScorerFactory
	key := os.Getenv(cfg.Jev.APIKeyEnv)
	if key == "" {
		fmt.Fprintf(os.Stderr, "%s not set; jevci strategy will run fail-open\n", cfg.Jev.APIKeyEnv)
	} else {
		factory = func() semantic.Scorer { return jev.New(cfg.Jev, key, &semantic.Usage{}) }
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	f, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	records, err := benchmark.Run(ctx, suite, cfg, factory, f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	failed := false
	for _, r := range records {
		status := "ok"
		if r.Error != "" {
			status = "error: " + r.Error
		}
		fmt.Fprintf(os.Stderr, "PR %d: %s\n", r.PR, status)
		if r.Error != "" {
			failed = true
		}
		for _, sm := range r.Strategies {
			if sm.Error != "" {
				failed = true
			}
		}
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d records)\n", *outPath, len(records))
	if failed {
		return 1
	}
	return 0
}

func runReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	in := fs.String("in", "", "results JSONL (required)")
	md := fs.String("md", "", "write markdown report to this file (default stdout)")
	asJSON := fs.Bool("json", false, "dump the aggregate summary as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *in == "" {
		fmt.Fprintln(os.Stderr, "error: --in is required")
		fs.Usage()
		return 2
	}
	records, err := benchmark.LoadRecords(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *asJSON {
		if err := benchmark.WriteAggregateJSON(os.Stdout, records); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}
	var w *os.File
	if *md != "" {
		w, err = os.Create(*md)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer w.Close()
	} else {
		w = os.Stdout
	}
	benchmark.WriteMarkdown(w, records)
	return 0
}

func runCalibration(args []string) int {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	suitePath := fs.String("suite", "", "suite YAML with pinned commits (required)")
	oraclePath := fs.String("oracle", "", "measured revert-oracle JSONL (required)")
	outPath := fs.String("out", "", "new calibration JSONL file (required; must not exist)")
	cfgPath := fs.String("config", "", "config file (default: suite config or .jevci.yaml)")
	timeout := fs.Duration("timeout", 30*time.Minute, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *suitePath == "" || *oraclePath == "" || *outPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: --suite, --oracle and --out are required; positional arguments are not supported")
		return 2
	}
	suite, err := benchmark.LoadSuite(*suitePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	cfgFile := *cfgPath
	if cfgFile == "" {
		cfgFile = suite.Config
	}
	if cfgFile == "" {
		cfgFile = ".jevci.yaml"
	}
	cfg, err := config.Load(cfgFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	key := os.Getenv(cfg.Jev.APIKeyEnv)
	if !cfg.Jev.Enabled || key == "" {
		fmt.Fprintf(os.Stderr, "error: calibration requires Jev enabled and %s set; no fail-open scores\n", cfg.Jev.APIKeyEnv)
		return 1
	}
	records, err := benchmark.LoadRecords(*oraclePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	f, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	factory := func() semantic.Scorer { return jev.New(cfg.Jev, key, &semantic.Usage{}) }
	runErr := benchmark.Calibrate(ctx, suite, cfg, records, factory, f)
	closeErr := f.Close()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "error:", runErr)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintln(os.Stderr, "error:", closeErr)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d PRs); no tests or oracle reruns\n", *outPath, len(suite.PRs))
	return 0
}
