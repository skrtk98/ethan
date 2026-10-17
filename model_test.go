package ethan_test

import (
	"testing"

	"github.com/skrtk98/ethan"
)

func TestTextLeavesOutNarrationBetweenToolUses(t *testing.T) {
	resp := &ethan.Response{Parts: []ethan.Part{
		{Text: "Let me search."},
		{Raw: "server_tool_use"},
		{Raw: "web_search_tool_result"},
		{Text: "Results are JSON strings; parse."},
		{Raw: "server_tool_use"},
		{Text: "The answer "},
		{Text: "with a citation."},
	}}
	if got := resp.Text(); got != "The answer with a citation." {
		t.Errorf("Text = %q", got)
	}
	plain := &ethan.Response{Parts: []ethan.Part{{Text: "a"}, {Text: "b"}}}
	if got := plain.Text(); got != "ab" {
		t.Errorf("Text = %q", got)
	}
}
