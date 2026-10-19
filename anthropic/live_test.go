//go:build live

package anthropic_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/skrtk98/ethan"
	"github.com/skrtk98/ethan/anthropic"
)

// TestLiveRun calls the real API. Run with: go test -tags live ./anthropic
func TestLiveRun(t *testing.T) {
	res, err := ethan.New(anthropic.New(), ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 100_000})).
		Run(context.Background(), "What is 17 * 23? Answer with the number only.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "391") {
		t.Errorf("Output = %q", res.Output)
	}
	t.Logf("usage: %+v", res.Usage)
}

// TestLivePromptCaching checks that a repeated long prompt is read from the
// cache. Run with: go test -tags live -run Caching ./anthropic
func TestLivePromptCaching(t *testing.T) {
	var sb strings.Builder
	for i := range 600 {
		fmt.Fprintf(&sb, "Rule %d: answer politely and briefly.\n", i)
	}
	req := ethan.Request{System: sb.String(), Messages: []ethan.Message{ethan.UserText("Say OK.")}, MaxTokens: 50}
	m := anthropic.New()
	var last ethan.Usage
	for range 2 {
		resp, err := m.Generate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		last = resp.Usage
		t.Logf("usage: %+v", resp.Usage)
	}
	if last.CacheReadTokens == 0 {
		t.Error("the second call read nothing from the cache")
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
	var got answer
	res, err := ethan.New(anthropic.New(), ethan.WithLimits(ethan.Limits{MaxDepth: 1, MaxTokens: 100_000})).
		Run(context.Background(), "What is 17 * 23? Answer with the number only.", ethan.Into(&got))
	if err != nil {
		t.Fatal(err)
	}
	if got.Product != 391 {
		t.Errorf("value = %s", res.Value)
	}
	t.Logf("value: %s, usage: %+v", res.Value, res.Usage)
}
