// Package output renders plans as a human table, JSON, or a package list.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/abdelrahmanmagdii/jevci/internal/planner"
	"github.com/abdelrahmanmagdii/jevci/internal/policy"
	"github.com/abdelrahmanmagdii/jevci/internal/semantic"
)

// Human writes the plan table.
func Human(w io.Writer, p *planner.Plan) {
	fmt.Fprintln(w, "JevCI")
	fmt.Fprintln(w)
	if len(p.ChangedPackages) > 0 {
		fmt.Fprintln(w, "Changed packages:")
		for _, c := range p.ChangedPackages {
			fmt.Fprintf(w, "  %s\n", c)
		}
		fmt.Fprintln(w)
	}
	if len(p.NestedModuleFiles) > 0 {
		fmt.Fprintln(w, "Nested module files (not tested by this module):")
		for _, f := range p.NestedModuleFiles {
			fmt.Fprintf(w, "  %s\n", f)
		}
		fmt.Fprintln(w)
	}
	if len(p.UnattributedFiles) > 0 {
		fmt.Fprintln(w, "Unattributed files:")
		for _, f := range p.UnattributedFiles {
			fmt.Fprintf(w, "  %s\n", f)
		}
		fmt.Fprintln(w)
	}
	if p.RunAllReason != "" {
		fmt.Fprintf(w, "Run all: %s\n\n", p.RunAllReason)
	}
	fmt.Fprintf(w, "%d test targets discovered.\n\n", p.TargetsTotal)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TEST TARGET\tSOURCE\tRELEVANCE\tACTION")
	hidden := 0
	for _, t := range p.Targets {
		if len(p.Targets) > 60 && t.Decision.Action == policy.Skip {
			hidden++
			continue
		}
		src := sourceName(t.Decision.Source)
		fmt.Fprintf(tw, "%s\t%s\t%d%%\t%s\n", t.Package, src, int(t.Decision.Relevance*100+0.5), t.Decision.Action)
	}
	tw.Flush()
	if hidden > 0 {
		fmt.Fprintf(w, "(+ %d skipped targets hidden; use --json for all)\n", hidden)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Selected: %d / %d targets\n", p.TargetsSelected, p.TargetsTotal)
	fmt.Fprintf(w, "Reduction: %.1f%%\n", p.ReductionPercent)
	if p.Jev.Enabled {
		fmt.Fprintf(w, "Jev planning time: %.1fs\n", float64(p.Jev.LatencyMS)/1000)
	}
	if p.Jev.Error != "" {
		fmt.Fprintf(w, "warning: Jev unavailable (%s); running all candidate packages (fail-open)\n", p.Jev.Error)
	}
}

func sourceName(s policy.Source) string {
	if s == policy.SourceJev {
		return "Jev"
	}
	return string(s)
}

// JSON writes the stable machine-readable schema.
func JSON(w io.Writer, p *planner.Plan) error {
	type target struct {
		Package    string   `json:"package"`
		ImportPath string   `json:"import_path"`
		Class      string   `json:"class"`
		Distance   int      `json:"distance"`
		Via        []string `json:"via"`
		Action     string   `json:"action"`
		Source     string   `json:"source"`
		Relevance  float64  `json:"relevance"`
		Reason     string   `json:"reason"`
	}
	doc := map[string]any{
		"version":             1,
		"base":                p.Base,
		"head":                p.Head,
		"merge_base":          p.MergeBase,
		"strategy":            p.Strategy,
		"changed_files":       orEmpty(p.ChangedFiles),
		"ignored_files":       orEmpty(p.IgnoredFiles),
		"unattributed_files":  orEmpty(p.UnattributedFiles),
		"nested_module_files": orEmpty(p.NestedModuleFiles),
		"changed_packages":    orEmpty(p.ChangedPackages),
		"selected_packages":   orEmpty(p.Selected),
		"skipped_packages":    orEmpty(p.Skipped),
		"run_all_reason":      p.RunAllReason,
		"targets_total":       p.TargetsTotal,
		"targets_selected":    p.TargetsSelected,
		"reduction_percent":   p.ReductionPercent,
		"jev": map[string]any{
			"enabled":      p.Jev.Enabled,
			"model":        p.Jev.Model,
			"requests":     p.Jev.Requests,
			"input_tokens": p.Jev.InputTokens,
			"candidates":   p.Jev.Candidates,
			"latency_ms":   p.Jev.LatencyMS,
			"cost_usd":     float64(p.Jev.InputTokens) / 1e6 * semantic.PricePerMillionInputTokensUSD,
			"error":        p.Jev.Error,
		},
		"jev_latency_ms": p.Jev.LatencyMS,
	}
	targets := make([]target, 0, len(p.Targets))
	for _, t := range p.Targets {
		targets = append(targets, target{
			Package: t.Package, ImportPath: t.ImportPath,
			Class: className(t.Class), Distance: t.Distance,
			Via: orEmpty(t.Via), Action: t.Decision.Action.String(),
			Source: string(t.Decision.Source), Relevance: t.Decision.Relevance,
			Reason: t.Decision.Reason,
		})
	}
	doc["targets"] = targets
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func className(c policy.StaticClass) string { return c.String() }

// Packages writes selected display paths, one per line.
func Packages(w io.Writer, p *planner.Plan) {
	for _, s := range p.Selected {
		fmt.Fprintln(w, s)
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
