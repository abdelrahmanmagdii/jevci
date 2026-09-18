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
	default:
		usage()
		return 2
	}
}

func runBench(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	suitePath := fs.String("suite", "", "suite YAML file (required)")
	outPath := fs.String("out", "", "results JSONL path (required)")
	noTests := fs.Bool("no-tests", false, "plan only; do not run go test")
	strategies := fs.String("strategies", "", "comma-separated strategy override")
	cfgPath := fs.String("config", ".jevci.yaml", "config file")
	timeout := fs.Duration("timeout", 60*time.Minute, "overall timeout")
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
	}
	if *strategies != "" {
		suite.Strategies = strings.Split(*strategies, ",")
	}
	cfg, err := config.Load(*cfgPath)
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
	f, err := os.Create(*outPath)
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
	for _, r := range records {
		status := "ok"
		if r.Error != "" {
			status = "error: " + r.Error
		}
		fmt.Fprintf(os.Stderr, "PR %d: %s\n", r.PR, status)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d records)\n", *outPath, len(records))
	return 0
}

func runReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	in := fs.String("in", "", "results JSONL (required)")
	md := fs.String("md", "", "write markdown report to this file (default stdout)")
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
