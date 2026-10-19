//go:build live

package openai_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/openai"
)

// The live tests call real APIs. Run with: go test -tags live ./openai
// They use OpenAI (OPENAI_API_KEY), and Ollama too if OLLAMA_MODEL names a
// local model.
func liveModels(t *testing.T) map[string]*openai.Model {
	models := map[string]*openai.Model{}
	if os.Getenv("OPENAI_API_KEY") != "" {
		models["openai"] = openai.New()
	}
	if id := os.Getenv("OLLAMA_MODEL"); id != "" {
		// Ollama ignores the key; do not send it the OpenAI one.
		models["ollama"] = openai.New(openai.WithBaseURL("http://localhost:11434/v1/"), openai.WithModel(id),
			openai.WithClientOptions(option.WithAPIKey("ollama")))
	}
	if len(models) == 0 {
		t.Skip("set OPENAI_API_KEY or OLLAMA_MODEL")
	}
	return models
}

func TestLiveRun(t *testing.T) {
	for name, m := range liveModels(t) {
		t.Run(name, func(t *testing.T) {
			res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 100_000})).
				Run(context.Background(), "What is 17 * 23? Answer with the number only.")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Output, "391") {
				t.Errorf("Output = %q", res.Output)
			}
			t.Logf("usage: %+v", res.Usage)
		})
	}
}

// multiply is a tool the model must use to answer.
type multiply struct{ calls *atomic.Int32 }

func (multiply) Spec() ethan.ToolSpec {
	return ethan.ToolSpec{
		Name:        "multiply",
		Description: "Multiplies two integers exactly.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}`),
	}
}

func (m multiply) Call(_ context.Context, input json.RawMessage) (string, error) {
	m.calls.Add(1)
	var in struct{ A, B int64 }
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	out, err := json.Marshal(in.A * in.B)
	return string(out), err
}

func TestLiveToolLoop(t *testing.T) {
	for name, m := range liveModels(t) {
		t.Run(name, func(t *testing.T) {
			tool := multiply{calls: new(atomic.Int32)}
			agent := ethan.Agent{Name: "calculator", Description: "Does arithmetic with its tools.",
				Instructions: "Always use the multiply tool for multiplication.", Tools: []ethan.Tool{tool}}
			res, err := ethan.New(m, ethan.WithAgents(agent), ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 100_000})).
				Run(context.Background(), "What is 123456789 * 987654321? Use the multiply tool and answer with the number only.")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.ReplaceAll(res.Output, ",", ""), "121932631112635269") || tool.calls.Load() == 0 {
				t.Errorf("Output = %q after %d tool calls", res.Output, tool.calls.Load())
			}
			t.Logf("usage: %+v", res.Usage)
		})
	}
}

// answer exercises the shapes SchemaFor emits: nested objects, arrays and
// nullable pointers.
type answer struct {
	Product int               `json:"product" desc:"the product"`
	Factors []int             `json:"factors"`
	Note    *string           `json:"note" desc:"null unless there is a caveat"`
	Check   struct{ OK bool } `json:"check"`
}

// TestLiveInto checks that the provider accepts the strict schema of Into.
func TestLiveInto(t *testing.T) {
	for name, m := range liveModels(t) {
		t.Run(name, func(t *testing.T) {
			var got answer
			res, err := ethan.New(m, ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 100_000})).
				Run(context.Background(), "What is 17 * 23? Answer with the number only.", ethan.Into(&got))
			if err != nil {
				t.Fatal(err)
			}
			if got.Product != 391 {
				t.Errorf("value = %s", res.Value)
			}
			t.Logf("value: %s, usage: %+v", res.Value, res.Usage)
		})
	}
}
