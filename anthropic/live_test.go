//go:build live

package anthropic_test

import (
	"context"
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
