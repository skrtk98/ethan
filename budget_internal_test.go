package ethan

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEstimateCoversEverythingSent(t *testing.T) {
	schema := json.RawMessage(`{"description":"` + strings.Repeat("x", 3000) + `"}`)
	base := Request{System: "s", Messages: []Message{UserText("hi")}, MaxTokens: 100}
	with := base
	with.Output = &OutputSchema{Name: "o", Schema: schema}

	if got := estimateTokens(with) - estimateTokens(base); got < 1500 {
		t.Errorf("a 3000-byte schema adds %d tokens to the estimate, want at least 1500", got)
	}
	jp := Request{Messages: []Message{UserText(strings.Repeat("日本語", 100))}}
	if got := estimateTokens(jp); got < 300 {
		t.Errorf("300 Japanese characters estimated at %d tokens, want at least 300", got)
	}
}
