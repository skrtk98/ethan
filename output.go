package ethan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Into has the Run, or the Resume, end by converting the goal's output into
// a value of type T, stored in *v and, as JSON, in Result.Value. The
// conversion is one more model call, made after the exploration finishes,
// with the schema SchemaFor[T]; T must be a struct.
//
// The value is journaled. Resuming a finished run reuses it if it still
// decodes into T, and converts the output again otherwise; resuming a run
// whose conversion failed only converts.
func Into[T any](v *T) RunOption {
	return func(r *run) {
		o := &into{decode: func(raw json.RawMessage) error {
			var t T
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&t); err != nil {
				return err
			}
			*v = t
			return nil
		}}
		if k := reflect.TypeFor[T]().Kind(); k != reflect.Struct {
			o.err = fmt.Errorf("ethan: Into: %v is not a struct", reflect.TypeFor[T]())
		} else if schema, err := SchemaFor[T](); err != nil {
			o.err = fmt.Errorf("ethan: Into: %w", err)
		} else {
			o.schema = &OutputSchema{Name: "result", Description: "The result of the goal.", Schema: schema}
		}
		r.into = o
	}
}

// into is the conversion Into asks for.
type into struct {
	schema *OutputSchema
	decode func(json.RawMessage) error
	err    error // why the conversion cannot be made
}

// convert ends a solved run as Into asks, setting r.value.
func (r *run) convert(ctx context.Context, root *Task) error {
	o := r.into
	if o == nil {
		return nil
	}
	if r.value != nil && o.decode(r.value) == nil {
		return nil // resumed
	}
	r.value = nil
	var raw json.RawMessage
	err := generateJSON(ctx, r.model, TagFormat, orDefault(nil, "format"), struct{ Goal, Result string }{r.goal, root.Output},
		o.schema, r.engine.maxTokens, &raw)
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	if err := o.decode(raw); err != nil {
		return fmt.Errorf("format: decode: %w", err)
	}
	r.value = raw
	return nil
}
