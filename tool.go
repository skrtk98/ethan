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
// Input with fields In does not have is rejected, and the error goes back
// to the model like any failed call.
func FuncTool[In any](name, description string, fn func(context.Context, In) (string, error)) Tool {
	if k := reflect.TypeFor[In]().Kind(); k != reflect.Struct {
		panic(fmt.Sprintf("ethan: FuncTool %s: input must be a struct, not %v", name, k))
	}
	schema, err := SchemaFor[In]()
	if err != nil {
		panic(fmt.Sprintf("ethan: FuncTool %s: %v", name, err))
	}
	return funcTool[In]{spec: ToolSpec{Name: name, Description: description, InputSchema: schema}, fn: fn}
}

type funcTool[In any] struct {
	spec ToolSpec
	fn   func(context.Context, In) (string, error)
}

func (t funcTool[In]) Spec() ToolSpec { return t.spec }

func (t funcTool[In]) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var in In
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return t.fn(ctx, in)
}
