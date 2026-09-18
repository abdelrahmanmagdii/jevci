// jevci computes which Go test packages to run for a base..head change.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/abdelrahmanmagdii/jevci/internal/config"
	"github.com/abdelrahmanmagdii/jevci/internal/output"
	"github.com/abdelrahmanmagdii/jevci/internal/planner"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic/jev"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  jevci plan --base <rev> [--head HEAD] [--repo .] [--config .jevci.yaml]
             [--json] [--packages] [--strategy jevci|static|changed|full]
             [--no-jev] [--timeout 2m]
  jevci version
`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println("jevci " + version)
		return 0
	case "plan":
		return runPlan(args[1:])
	default:
		usage()
		return 2
	}
}

func runPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	base := fs.String("base", "", "base revision (required)")
	head := fs.String("head", "HEAD", "head revision")
	repo := fs.String("repo", ".", "repository directory")
	cfgPath := fs.String("config", ".jevci.yaml", "config file path")
	asJSON := fs.Bool("json", false, "emit JSON")
	packages := fs.Bool("packages", false, "emit selected package list")
	strategy := fs.String("strategy", "jevci", "planning strategy")
	noJev := fs.Bool("no-jev", false, "disable Jev scoring")
	timeout := fs.Duration("timeout", 2*time.Minute, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *base == "" {
		fmt.Fprintln(os.Stderr, "error: --base is required")
		fs.Usage()
		return 2
	}
	st, err := policy.ParseStrategy(*strategy)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	// A --config left at its default resolves inside --repo when missing
	// from the cwd; an explicit path is used as given.
	cfgFile := *cfgPath
	if cfgFile == ".jevci.yaml" {
		if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
			cfgFile = filepath.Join(*repo, cfgFile)
		}
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

	var scorer semantic.Scorer
	if !*noJev && cfg.Jev.Enabled && st == policy.StrategyJevCI {
		key := os.Getenv(cfg.Jev.APIKeyEnv)
		if key == "" {
			fmt.Fprintf(os.Stderr, "%s not set; Jev disabled, running all static candidates (fail-open)\n", cfg.Jev.APIKeyEnv)
		} else {
			scorer = jev.New(cfg.Jev, key, &semantic.Usage{})
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	p, err := planner.Run(ctx, planner.Options{
		RepoDir: *repo, Base: *base, Head: *head,
		Strategy: st, Config: cfg, Scorer: scorer,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	switch {
	case *asJSON:
		if err := output.JSON(os.Stdout, p); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	case *packages:
		output.Packages(os.Stdout, p)
	default:
		output.Human(os.Stdout, p)
	}
	return 0
}
