package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type benchRun struct {
	Started  time.Time     `json:"started"`
	Finished time.Time     `json:"finished"`
	Complete bool          `json:"complete"` // false while running or if cut short
	Planned  int           `json:"planned"`  // runs the benchmark set out to do
	Timeout  time.Duration `json:"timeout_ns"`
	MaxTurns int           `json:"max_turns"`
	Results  []Result      `json:"results"`
	Path     string        `json:"-"`
}

func printResult(r Result) {
	mark := "✘"
	if r.Passed {
		mark = "✔"
	}
	note := ""
	switch {
	case r.TimedOut:
		note = " (timed out)"
	case r.Error != "":
		note = " (" + firstLine(r.Error) + ")"
	case !r.Passed:
		note = " (" + firstLine(r.Check) + ")"
	}
	fmt.Printf("%s %-22s %-10s %-14s %5.0fs  %2d tools%s\n", mark, r.Task, r.Agent, r.Model, r.Seconds, r.Tools, note)
}

// group is the results for one agent and model.
type group struct {
	Agent, Model string
	Results      []Result
}

func groups(rs []Result) []group {
	idx := map[string]int{}
	var out []group
	for _, r := range rs {
		k := r.Agent + "\x00" + r.Model
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, group{Agent: r.Agent, Model: r.Model})
		}
		out[i].Results = append(out[i].Results, r)
	}
	return out
}

type stats struct {
	Passed, Total, TimedOut, Errors int
	Seconds, Median                 float64
	Tokens, Tools                   int
}

func summarize(rs []Result) stats {
	var s stats
	var times []float64
	for _, r := range rs {
		s.Total++
		if r.Passed {
			s.Passed++
		}
		if r.TimedOut {
			s.TimedOut++
		}
		if r.Error != "" && !r.TimedOut {
			s.Errors++
		}
		s.Seconds += r.Seconds
		s.Tokens += r.Tokens
		s.Tools += r.Tools
		times = append(times, r.Seconds)
	}
	sort.Float64s(times)
	if n := len(times); n > 0 {
		s.Median = times[n/2]
		if n%2 == 0 {
			s.Median = (times[n/2-1] + times[n/2]) / 2
		}
	}
	return s
}

func printSummary(run *benchRun) {
	fmt.Println()
	for _, g := range groups(run.Results) {
		s := summarize(g.Results)
		fmt.Printf("%s on %s: solved %d/%d · %s total · median %.0fs · %d tool calls · %d tokens · %d timeouts · %d errors\n",
			g.Agent, g.Model, s.Passed, s.Total, dur(s.Seconds), s.Median, s.Tools, s.Tokens, s.TimedOut, s.Errors)
	}
}

func dur(sec float64) string {
	return (time.Duration(sec) * time.Second).Round(time.Second).String()
}

// save writes the run to its results file, replacing it atomically.
func (run *benchRun) save() error {
	if err := os.MkdirAll(filepath.Dir(run.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp := run.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, run.Path)
}

func loadRun(path string) (*benchRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var run benchRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	run.Path = path
	return &run, nil
}

// writeReport renders a run as markdown: a summary per agent and model,
// then a task-by-task grid.
func writeReport(path string, run *benchRun) error {
	gs := groups(run.Results)
	var b strings.Builder
	b.WriteString("# Benchmark\n\n")
	fmt.Fprintf(&b, "Run %s · %d tasks · up to %d model calls and %s per task.\n",
		run.Started.Format("2 January 2006 15:04"), len(taskOrder(run.Results)), run.MaxTurns, run.Timeout)
	if !run.Complete {
		fmt.Fprintf(&b, "\n**Unfinished run:** %d of %d planned runs completed.\n\n", len(run.Results), run.Planned)
	}
	b.WriteString("Reproduce with `make bench`. Tasks live in `bench/testdata/tasks`; each one is checked by a script, with the tests restored first so an agent cannot pass by editing them.\n\n")

	b.WriteString("## Summary\n\n| Agent | Model | Solved | Total time | Median | Tool calls | Tokens | Timeouts | Errors |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, g := range gs {
		s := summarize(g.Results)
		fmt.Fprintf(&b, "| %s | %s | **%d/%d** | %s | %.0fs | %d | %d | %d | %d |\n",
			g.Agent, g.Model, s.Passed, s.Total, dur(s.Seconds), s.Median, s.Tools, s.Tokens, s.TimedOut, s.Errors)
	}

	b.WriteString("\n## By task\n\n| Task | Kind |")
	for _, g := range gs {
		fmt.Fprintf(&b, " %s · %s |", g.Agent, g.Model)
	}
	b.WriteString("\n|---|---|" + strings.Repeat("---|", len(gs)) + "\n")
	for _, id := range taskOrder(run.Results) {
		kind := ""
		row := ""
		for _, g := range gs {
			cell := "–"
			for _, r := range g.Results {
				if r.Task != id {
					continue
				}
				kind = r.Kind
				mark := "✘"
				if r.Passed {
					mark = "✔"
				}
				cell = fmt.Sprintf("%s %.0fs", mark, r.Seconds)
				if r.TimedOut {
					cell += " (timeout)"
				}
			}
			row += " " + cell + " |"
		}
		fmt.Fprintf(&b, "| %s | %s |%s\n", id, kind, row)
	}

	var failures []Result
	for _, r := range run.Results {
		if !r.Passed {
			failures = append(failures, r)
		}
	}
	if len(failures) > 0 {
		b.WriteString("\n## Why tasks failed\n\n")
		for _, r := range failures {
			why := firstLine(r.Check)
			switch {
			case r.TimedOut:
				why = "timed out"
			case r.Error != "":
				why = firstLine(r.Error)
			}
			fmt.Fprintf(&b, "- **%s** (%s, %s): %s\n", r.Task, r.Agent, r.Model, strings.ReplaceAll(why, "|", "\\|"))
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func taskOrder(rs []Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs {
		if !seen[r.Task] {
			seen[r.Task] = true
			out = append(out, r.Task)
		}
	}
	return out
}
