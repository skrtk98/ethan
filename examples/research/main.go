// Command research explores a research question with E.T.H.A.N. and Claude
// and writes a report.
//
//	go run ./examples/research -o report.md -journal run.jsonl "question"
//
// A run that stops, for a limit or otherwise, can be continued from its
// journal:
//
//	go run ./examples/research -o report.md -journal run.jsonl -resume
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/anthropic"
)

func main() {
	var (
		out         = flag.String("o", "", "write the report to this file instead of stdout")
		tracePath   = flag.String("trace", "", "write the JSON trace to this file")
		journalPath = flag.String("journal", "", "record the run's events to this file so that it can be resumed")
		resume      = flag.Bool("resume", false, "resume the run recorded in -journal instead of starting one")
		model       = flag.String("model", anthropic.DefaultModel, "Claude model id")
		effort      = flag.String("effort", "", "effort level: low, medium, high, xhigh or max")
		maxDepth    = flag.Int("max-depth", 2, "depth at which tasks are no longer decomposed")
		maxTasks    = flag.Int("max-tasks", 15, "tasks the run may start, leaves included")
		maxTokens   = flag.Int("max-tokens", 1_000_000, "total token limit (0 = unlimited)")
		concurrency = flag.Int("concurrency", 4, "model calls in flight at once")
		plans       = flag.Int("plans", 3, "alternative plans proposed per task")
		beam        = flag.Int("beam", 2, "plans executed in parallel at the root")
		deepBeam    = flag.Int("deep-beam", 1, "plans executed in parallel below the root")
		searches    = flag.Int("searches", 5, "web searches allowed per model call")
		quiet       = flag.Bool("q", false, "do not print progress to stderr")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: research [flags] question\n       research [flags] -journal file -resume\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	question := strings.Join(flag.Args(), " ")
	if *resume && *journalPath == "" || !*resume && question == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	common := []anthropic.Option{anthropic.WithModel(*model)}
	if *effort != "" {
		common = append(common, anthropic.WithEffort(*effort))
	}
	control := anthropic.New(common...)
	searcher := anthropic.New(append(common, anthropic.WithWebSearch(*searches))...)

	opts := []ethan.Option{
		ethan.WithAgents(
			ethan.Agent{
				Name:        "researcher",
				Description: "Searches the web and reports sourced findings on a focused question.",
				Instructions: "You are a meticulous researcher. Search the web, prefer primary and recent sources, " +
					"and cite the URL of every claim. Say plainly when evidence is missing or conflicting.",
				Model: searcher,
			},
			ethan.Agent{
				Name:         "analyst",
				Description:  "Compares, analyzes and draws conclusions from material it is given; does not search.",
				Instructions: "You are a careful analyst. Reason only from the inputs and notes you are given and keep their citations.",
			},
		),
		ethan.WithLimits(ethan.Limits{MaxDepth: *maxDepth, MaxTasks: *maxTasks, MaxTokens: *maxTokens, MaxConcurrency: *concurrency}),
		ethan.WithMaxPlans(*plans),
		ethan.WithStrategy(ethan.BeamStrategy{Width: *beam, DeepWidth: *deepBeam}),
	}
	if !*quiet {
		opts = append(opts, ethan.WithObserver(ethan.ObserverFunc(progress)))
	}
	var events []ethan.Event
	if *resume {
		var err error
		if events, err = ethan.ReadJournal(*journalPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
	if *journalPath != "" {
		j, err := ethan.CreateJournal(*journalPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer j.Close()
		opts = append(opts, ethan.WithJournal(j))
	}

	engine := ethan.New(control, opts...)
	var (
		res *ethan.Result
		err error
	)
	if *resume {
		res, err = engine.Resume(ctx, events)
	} else {
		res, err = engine.Run(ctx, question)
	}
	if res == nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if *tracePath != "" {
		if terr := writeTrace(*tracePath, res); terr != nil {
			fmt.Fprintln(os.Stderr, "trace:", terr)
		}
	}
	fmt.Fprintf(os.Stderr, "tokens: %d in, %d out\n", res.Usage.InputTokens, res.Usage.OutputTokens)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if *journalPath != "" {
			fmt.Fprintf(os.Stderr, "continue with: research -journal %s -resume\n", *journalPath)
		}
		os.Exit(1)
	}

	if *out == "" {
		fmt.Println(res.Output)
		return
	}
	if err := os.WriteFile(*out, []byte(res.Output+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func writeTrace(path string, res *ethan.Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := res.WriteTrace(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// progress prints a one-line summary of each event.
func progress(e ethan.Event) {
	id := e.TaskID
	if e.PlanID != "" {
		id = e.PlanID
	}
	line := fmt.Sprintf("%-14s %-16s", e.Kind, id)
	if e.Score != nil {
		line += fmt.Sprintf(" score=%.2f", e.Score.Value)
	}
	if e.Usage != nil {
		line += fmt.Sprintf(" tokens=%d", e.Usage.Total())
	}
	switch e.Kind {
	case ethan.EventTaskStarted, ethan.EventTaskDecided, ethan.EventPlanStarted, ethan.EventNoteAdded,
		ethan.EventTaskFailed, ethan.EventPlanFailed:
		line += " " + clip(e.Text, 90)
	}
	fmt.Fprintln(os.Stderr, line)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
