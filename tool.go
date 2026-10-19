package ethan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// FuncTool returns a Tool that decodes its input into In and calls fn. The
// input schema is SchemaFor[In], so In must be a struct. FuncTool panics if
// it is not, or if SchemaFor fails.
//
// fn is called only with input that matches the schema: every field
// present, no others, each of the right type. Providers do not all enforce
// the schema, so FuncTool checks it; a mismatch goes back to the model like
// any failed call.
func FuncTool[In any](name, description string, fn func(context.Context, In) (string, error)) Tool {
	t := reflect.TypeFor[In]()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("ethan: FuncTool %s: input must be a struct, not %v", name, t))
	}
	s, err := schemaOf(t, nil)
	if err != nil {
		panic(fmt.Sprintf("ethan: FuncTool %s: schema of %v: %v", name, t, err))
	}
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return funcTool[In]{spec: ToolSpec{Name: name, Description: description, InputSchema: raw}, schema: s, fn: fn}
}

type funcTool[In any] struct {
	spec   ToolSpec
	schema map[string]any
	fn     func(context.Context, In) (string, error)
}

func (t funcTool[In]) Spec() ToolSpec { return t.spec }

func (t funcTool[In]) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := validate(t.schema, v, "input"); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	var in In
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return t.fn(ctx, in)
}
