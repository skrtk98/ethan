package ethan_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skrtk98/ethan"
)

type base struct {
	ID string `json:"id"`
}

type query struct {
	base
	Text    string            `json:"text" desc:"what to search for"`
	Limit   *int              `json:"limit"`
	Tags    []string          `json:"tags,omitempty"`
	Count   int64             `json:"count,string"`
	Since   time.Time         `json:"since"`
	Score   float64           `json:"score"`
	OK      bool              `json:"ok"`
	Skipped string            `json:"-"`
	Inner   struct{ N uint8 } `json:"inner"`
	private int
}

func TestSchemaFor(t *testing.T) {
	got, err := ethan.SchemaFor[query]()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"additionalProperties":false,"properties":{` +
		`"count":{"type":"string"},` +
		`"id":{"type":"string"},` +
		`"inner":{"additionalProperties":false,"properties":{"N":{"type":"integer"}},"required":["N"],"type":"object"},` +
		`"limit":{"anyOf":[{"type":"integer"},{"type":"null"}]},` +
		`"ok":{"type":"boolean"},` +
		`"score":{"type":"number"},` +
		`"since":{"format":"date-time","type":"string"},` +
		`"tags":{"items":{"type":"string"},"type":"array"},` +
		`"text":{"description":"what to search for","type":"string"}},` +
		`"required":["id","text","limit","tags","count","since","score","ok","inner"],"type":"object"}`
	if string(got) != want {
		t.Errorf("schema =\n%s\nwant\n%s", got, want)
	}
}

type node struct {
	Children []node `json:"children"`
}

func TestSchemaForRejectsWhatStrictSchemasCannotDescribe(t *testing.T) {
	for name, f := range map[string]func() (json.RawMessage, error){
		"map":       ethan.SchemaFor[struct{ M map[string]int }],
		"interface": ethan.SchemaFor[struct{ V any }],
		"recursive": ethan.SchemaFor[node],
	} {
		if _, err := f(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFuncTool(t *testing.T) {
	tool := ethan.FuncTool("search", "Searches.", func(_ context.Context, q struct {
		Text string `json:"text"`
	}) (string, error) {
		return "found " + q.Text, nil
	})
	if spec := tool.Spec(); spec.Name != "search" || !strings.Contains(string(spec.InputSchema), `"text"`) {
		t.Errorf("spec = %+v", spec)
	}
	if out, err := tool.Call(context.Background(), json.RawMessage(`{"text":"go"}`)); err != nil || out != "found go" {
		t.Errorf("Call = %q, %v", out, err)
	}
	if _, err := tool.Call(context.Background(), json.RawMessage(`{"txt":"go"}`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestFuncToolPanicsOnANonStructInput(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	ethan.FuncTool("bad", "", func(context.Context, string) (string, error) { return "", nil })
}
