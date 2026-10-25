package ethan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Into has the Run, or the Resume, end by converting the goal's output into
// a value of type T, stored in *value and, as JSON, in Result.Value. The
// conversion is one more model call, made after the exploration finishes,
// with the schema SchemaFor[T]; T must be a struct.
//
// The value is journaled. Resuming a finished run reuses it if it still
// decodes into T, and converts the output again otherwise; resuming a run
// whose conversion failed only converts.
func Into[T any](value *T) RunOption {
	return func(r *run) {
		o := &into{}
		t := reflect.TypeFor[T]()
		s, err := schemaOf(t, nil)
		switch {
		case value == nil:
			o.err = errors.New("ethan: Into: nil pointer")
		case t.Kind() != reflect.Struct:
			o.err = fmt.Errorf("ethan: Into: %v is not a struct", t)
		case err != nil:
			o.err = fmt.Errorf("ethan: Into: schema of %v: %w", t, err)
		default:
			raw, err := json.Marshal(s)
			if err != nil {
				panic(err)
			}
			o.schema = &OutputSchema{Name: "result", Description: "The result of the goal.", Schema: raw}
			o.decode = func(raw json.RawMessage) error {
				var v any
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&v); err != nil {
					return err
				}
				if err := validate(s, v, "value"); err != nil {
					return err
				}
				var t T
				if err := json.Unmarshal(raw, &t); err != nil {
					return err
				}
				*value = t
				return nil
			}
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
		o.schema, r.engine.maxOutputTokens, &raw)
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	if err := o.decode(raw); err != nil {
		return fmt.Errorf("format: decode: %w", err)
	}
	r.value = raw
	return nil
}
