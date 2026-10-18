// Command research explores a research question with E.T.H.A.N. and Claude
// and writes a report.
//
//	go run ./examples/research -o report.md -journal run.jsonl "question"
//
// A run that stops, for a limit or otherwise, can be continued from its
// journal:
//
//	go run ./examples/research -o report.md -journal run.jsonl -resume
//
// -provider selects Claude (anthropic, the default), OpenAI (openai) or a
// local Ollama server (ollama, without web search).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"strings"

	openaiopt "github.com/openai/openai-go/v3/option"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/anthropic"
	"github.com/skrtk98/ethan/openai"
)

func main() {
	var (
		out         = flag.String("o", "", "write the report to this file instead of stdout")
		tracePath   = flag.String("trace", "", "write the JSON trace to this file")
		journalPath = flag.String("journal", "", "record the run's events to this file so that it can be resumed")
		resume      = flag.Bool("resume", false, "resume the run recorded in -journal instead of starting one")
		provider    = flag.String("provider", "anthropic", "model provider: anthropic, openai or ollama")
		model       = flag.String("model", "", "model id (default: the provider's; gpt-oss:20b for ollama)")
		baseURL     = flag.String("base-url", "http://localhost:11434/v1/", "server URL for -provider ollama")
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

	control, searcher, err := models(*provider, *model, *effort, *baseURL, *searches)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

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
	var (
		events  []ethan.Event
		runOpts []ethan.RunOption
	)
	if *resume {
		var err error
		if events, err = ethan.ReadJournal(*journalPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if len(events) == 0 {
			fmt.Fprintf(os.Stderr, "error: %s holds no run: it stopped before starting; delete the file and start a new run\n", *journalPath)
			os.Exit(1)
		}
	}
	if *journalPath != "" {
		// A journal holds one run: a new run gets a file of its own.
		open := ethan.NewJournal
		if *resume {
			open = ethan.CreateJournal
		}
		j, err := open(*journalPath)
		if errors.Is(err, fs.ErrExist) {
			err = fmt.Errorf("%s already holds a run; resume it with -resume or choose another file", *journalPath)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer j.Close()
		runOpts = append(runOpts, ethan.JournalTo(j))
	}

	engine := ethan.New(control, opts...)
	var res *ethan.Result
	if *resume {
		res, err = engine.Resume(ctx, events, runOpts...)
	} else {
		res, err = engine.Run(ctx, question, runOpts...)
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

// models returns the model for planning, evaluation and analysis, and the
// one the researcher searches the web with.
func models(provider, id, effort, baseURL string, searches int) (control, searcher ethan.Model, err error) {
	switch provider {
	case "anthropic":
		opts := []anthropic.Option{}
		if id != "" {
			opts = append(opts, anthropic.WithModel(id))
		}
		if effort != "" {
			opts = append(opts, anthropic.WithEffort(effort))
		}
		return anthropic.New(opts...), anthropic.New(append(opts, anthropic.WithWebSearch(searches))...), nil
	case "openai":
		opts := []openai.Option{}
		if id != "" {
			opts = append(opts, openai.WithModel(id))
		}
		if effort != "" {
			opts = append(opts, openai.WithEffort(effort))
		}
		return openai.New(opts...), openai.New(append(opts, openai.WithWebSearch(searches))...), nil
	case "ollama":
		if id == "" {
			id = "gpt-oss:20b"
		}
		// Ollama ignores the key; do not send it the OpenAI one.
		opts := []openai.Option{openai.WithBaseURL(baseURL), openai.WithModel(id),
			openai.WithClientOptions(openaiopt.WithAPIKey("ollama"))}
		if effort != "" {
			opts = append(opts, openai.WithEffort(effort))
		}
		fmt.Fprintln(os.Stderr, "warning: ollama has no web search; the researcher answers from what the model knows")
		m := openai.New(opts...)
		return m, m, nil
	}
	return nil, nil, fmt.Errorf("unknown provider %q", provider)
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
	if e.Kind == ethan.EventUsage {
		return // one per model call; task_done and task_failed sum them up
	}
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
