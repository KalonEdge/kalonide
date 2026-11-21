// Command bench is Kalonide's scoreboard: it runs a coding agent on a fixed
// set of real tasks and checks each result automatically.
//
//	go run ./bench -validate                 # prove every check is sound
//	go run ./bench -model gemma4:4b          # run ./kalonide on all tasks
//	go run ./bench -agent base=/tmp/old -agent new=./kalonide -report
//
// Each task lives in bench/testdata/tasks/NN-name/:
//
//	task.json   id, kind and the prompt given to the agent
//	repo/       the project the agent works in (copied fresh for every run)
//	hidden/     tests copied back over the result before checking, so an
//	            agent cannot pass by editing the tests
//	check.sh    exits 0 when the task is solved; runs in the work folder with
//	            TASK_DIR, ANSWER_FILE (the agent's final reply) and TMPDIR set
//	solution/   a known-good result, used by -validate
//	mutant/     optional files a check may swap in (e.g. to test the tests)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type agentFlag []string

func (a *agentFlag) String() string     { return strings.Join(*a, ",") }
func (a *agentFlag) Set(v string) error { *a = append(*a, v); return nil }

func main() {
	var (
		agents   agentFlag
		models   = flag.String("model", "", "comma-separated models to run (default: the agent's configured model)")
		only     = flag.String("tasks", "", "comma-separated task ids or number prefixes to run (default: all)")
		timeout  = flag.Duration("timeout", 6*time.Minute, "time limit per task run")
		turns    = flag.Int("max-turns", 30, "model calls allowed per task")
		repeat   = flag.Int("repeat", 1, "runs per task, to smooth out model randomness")
		validate = flag.Bool("validate", false, "check the tasks themselves instead of running an agent")
		report   = flag.Bool("report", false, "also write docs/BENCH.md from this run")
		render   = flag.String("render", "", "write docs/BENCH.md from a saved results file, then exit")
		taskDir  = flag.String("dir", "bench/testdata/tasks", "task folder")
		outDir   = flag.String("out", "bench/results", "where result files go")
	)
	flag.Var(&agents, "agent", "label=path of a kalonide binary to run (repeatable; default kalonide=./kalonide)")
	flag.Parse()

	if *render != "" {
		run, err := loadRun(*render)
		if err != nil {
			fail(err)
		}
		if err := writeReport("docs/BENCH.md", run); err != nil {
			fail(err)
		}
		fmt.Println("report: docs/BENCH.md")
		return
	}

	tasks, err := loadTasks(*taskDir, splitComma(*only))
	if err != nil {
		fail(err)
	}
	if *validate {
		if !validateTasks(tasks) {
			os.Exit(1)
		}
		return
	}

	if len(agents) == 0 {
		agents = agentFlag{"kalonide=./kalonide"}
	}
	var specs []agentSpec
	for _, a := range agents {
		s, err := parseAgent(a)
		if err != nil {
			fail(err)
		}
		specs = append(specs, s)
	}
	modelList := splitComma(*models)
	if len(modelList) == 0 {
		modelList = []string{""}
	}

	run := &benchRun{
		Started: time.Now(), Timeout: *timeout, MaxTurns: *turns,
		Planned: len(specs) * len(modelList) * *repeat * len(tasks),
	}
	run.Path = filepath.Join(*outDir, run.Started.Format("2006-01-02-1504")+"-"+runLabel(specs, modelList)+".json")
	// Results are saved after every task, so a run that is cut short (the
	// session ends, the machine sleeps) keeps everything done so far.
	for _, ag := range specs {
		for _, model := range modelList {
			for i := 0; i < *repeat; i++ {
				for _, t := range tasks {
					r := runTask(ag, model, t, *timeout, *turns)
					printResult(r)
					if serverGone(r) {
						// Later runs would all fail the same way: stop, keeping
						// what was done, marked unfinished.
						fail(fmt.Errorf("the model server stopped answering during %s; kept %d finished runs in %s, marked unfinished", t.ID, len(run.Results), run.Path))
					}
					run.Results = append(run.Results, r)
					if err := run.save(); err != nil {
						fail(err)
					}
				}
			}
		}
	}
	run.Finished = time.Now()
	run.Complete = true
	if err := run.save(); err != nil {
		fail(err)
	}
	printSummary(run)
	fmt.Println("results:", run.Path)
	if *report {
		if err := writeReport("docs/BENCH.md", run); err != nil {
			fail(err)
		}
		fmt.Println("report: docs/BENCH.md")
	}
}

type agentSpec struct {
	Label string
	Bin   string
}

func parseAgent(v string) (agentSpec, error) {
	label, path, ok := strings.Cut(v, "=")
	if !ok || label == "" || path == "" {
		return agentSpec{}, fmt.Errorf("-agent wants label=path, got %q", v)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return agentSpec{}, err
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		return agentSpec{}, fmt.Errorf("agent %s: no binary at %s (run make build)", label, abs)
	}
	return agentSpec{Label: label, Bin: abs}, nil
}

// runLabel names a results file after the agent and model when there is
// only one of each.
func runLabel(specs []agentSpec, models []string) string {
	if len(specs) == 1 && len(models) == 1 && models[0] != "" {
		return specs[0].Label + "-" + strings.ReplaceAll(models[0], ":", "_")
	}
	return "run"
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	os.Exit(1)
}
