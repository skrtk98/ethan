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
	//lint:ignore U1000 an unexported field must be left out of the schema
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

type left struct {
	Name string `json:"name"`
	Kept int
}

type right struct {
	Name string
	Kept int
}

func TestSchemaForResolvesEmbeddedNamesLikeEncodingJSON(t *testing.T) {
	type pair struct {
		left
		right
	}
	type shallow struct {
		left
		Kept string
	}
	type taggedKept struct {
		Kept string `json:"Kept"`
	}
	type tagged struct {
		taggedKept
		right
	}
	type recursive struct {
		//lint:ignore U1000 the embedding is what the test rejects
		*recursive
		N int
	}
	// Kept is untagged on both sides at the same depth.
	if _, err := ethan.SchemaFor[pair](); err == nil || !strings.Contains(err.Error(), "ambiguous field Kept") {
		t.Errorf("pair: err = %v", err)
	}
	s, err := ethan.SchemaFor[shallow]()
	if err != nil || !strings.Contains(string(s), `"Kept":{"type":"string"}`) || !strings.Contains(string(s), `"name":{"type":"string"}`) {
		t.Errorf("shallow: %s, %v", s, err)
	}
	// At the same depth the tagged Kept wins.
	if s, err := ethan.SchemaFor[tagged](); err != nil || !strings.Contains(string(s), `"Kept":{"type":"string"}`) {
		t.Errorf("tagged: %s, %v", s, err)
	}
	if _, err := ethan.SchemaFor[recursive](); err == nil {
		t.Error("recursive embedding accepted")
	}
}

func TestSchemaForMatchesEncodingJSONEncodings(t *testing.T) {
	s, err := ethan.SchemaFor[struct {
		Fixed [2]byte `json:"fixed"`
		Bytes []byte  `json:"bytes"`
		N     *int    `json:"n,string"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"fixed":{"items":{"type":"integer"},"type":"array"}`,
		`"bytes":{"description":"base64-encoded bytes","type":"string"}`,
		`"n":{"anyOf":[{"type":"string"},{"type":"null"}]}`,
	} {
		if !strings.Contains(string(s), want) {
			t.Errorf("schema %s lacks %s", s, want)
		}
	}
}

func TestFuncToolChecksItsInputAgainstTheSchema(t *testing.T) {
	called := false
	tool := ethan.FuncTool("transfer", "", func(context.Context, struct {
		To     string  `json:"to"`
		Amount int     `json:"amount"`
		Memo   *string `json:"memo"`
	}) (string, error) {
		called = true
		return "ok", nil
	})
	for _, input := range []string{`null`, `{}`, `{"to":"a","amount":1}`, `{"to":"a","amount":1.5,"memo":null}`, `{"to":1,"amount":1,"memo":null}`} {
		if _, err := tool.Call(context.Background(), json.RawMessage(input)); err == nil {
			t.Errorf("%s accepted", input)
		}
	}
	if called {
		t.Error("fn ran on invalid input")
	}
	if out, err := tool.Call(context.Background(), json.RawMessage(`{"to":"a","amount":1,"memo":null}`)); err != nil || out != "ok" {
		t.Errorf("valid input: %q, %v", out, err)
	}
}

func TestSchemaForFollowsEncodingJSONForNumbersAndBadTags(t *testing.T) {
	s, err := ethan.SchemaFor[struct {
		N json.Number `json:"n"`
		//lint:ignore SA5008 encoding/json ignores the malformed tag, as SchemaFor must
		Bad int `json:"a\"b"`
	}]()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s), `"n":{"type":"number"}`) || !strings.Contains(string(s), `"Bad":{"type":"integer"}`) {
		t.Errorf("schema = %s", s)
	}
}
