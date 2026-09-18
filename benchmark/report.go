package benchmark

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// WriteMarkdown renders a per-strategy summary table plus a per-PR table.
func WriteMarkdown(w io.Writer, records []Record) {
	sums := Aggregate(records)
	fmt.Fprintln(w, "| strategy | PRs | mean reduction % | mean runtime reduction % | failed total | detected | missed | recall | mean Jev latency ms | Jev cost USD | errors |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range sums {
		fmt.Fprintf(w, "| %s | %d | %.1f | %.1f | %d | %d | %d | %.2f | %.0f | %.4f | %d |\n",
			s.Strategy, s.PRs, s.MeanReductionPercent, s.MeanRuntimeReductionPct,
			s.FailedTotal, s.FailedDetected, s.FailedMissed, s.Recall,
			s.MeanJevLatencyMS, s.TotalJevCostUSD, s.Errors)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PR\tstrategy\tselected/total\treduction %\trecall")
	for _, r := range records {
		for _, sm := range r.Strategies {
			recall := "n/a"
			if sm.Recall != nil {
				recall = fmt.Sprintf("%.2f", *sm.Recall)
			}
			fmt.Fprintf(tw, "%d\t%s\t%d/%d\t%.1f\t%s\n",
				r.PR, sm.Strategy, sm.Selected, sm.TargetsTotal, sm.ReductionPercent, recall)
		}
	}
	tw.Flush()
}
