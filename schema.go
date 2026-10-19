package ethan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// SchemaFor returns the JSON Schema of the values of T, as encoding/json
// writes them, for tool inputs and structured output.
//
// The schema satisfies the strict modes of the providers: every field is
// required and objects allow no other properties. A pointer field may be
// null, which is how a value is left out. Field descriptions come from a
// desc struct tag:
//
//	type Query struct {
//		Text  string `json:"text" desc:"what to search for"`
//		Limit *int   `json:"limit" desc:"at most this many results"`
//	}
//
// Structs, strings, booleans, numbers, slices, arrays, pointers and
// time.Time are supported. Maps, interfaces and recursive types are not,
// since a strict schema cannot describe them.
func SchemaFor[T any]() (json.RawMessage, error) {
	s, err := schemaOf(reflect.TypeFor[T](), nil)
	if err != nil {
		return nil, fmt.Errorf("ethan: schema of %v: %w", reflect.TypeFor[T](), err)
	}
	return json.Marshal(s)
}

var timeType = reflect.TypeFor[time.Time]()

// schemaOf builds the schema of t; seen holds the struct types being built,
// to reject recursion.
func schemaOf(t reflect.Type, seen []reflect.Type) (map[string]any, error) {
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Pointer:
		s, err := schemaOf(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}, nil
		}
		items, err := schemaOf(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		s := map[string]any{"type": "array", "items": items}
		if t.Kind() == reflect.Array {
			s["minItems"], s["maxItems"] = t.Len(), t.Len()
		}
		return s, nil
	case reflect.Struct:
		for _, st := range seen {
			if st == t {
				return nil, fmt.Errorf("recursive type %v", t)
			}
		}
		props := map[string]any{}
		required := []string{}
		if err := addFields(t, append(seen, t), props, &required); err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, nil
	default:
		return nil, fmt.Errorf("unsupported type %v", t)
	}
}

// addFields adds the fields of struct t to props, flattening embedded
// structs as encoding/json does.
func addFields(t reflect.Type, seen []reflect.Type, props map[string]any, required *[]string) error {
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" && opts == "" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				if err := addFields(ft, seen, props, required); err != nil {
					return err
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		s, err := schemaOf(f.Type, seen)
		if err != nil {
			return fmt.Errorf("field %s: %w", f.Name, err)
		}
		if slices.Contains(strings.Split(opts, ","), "string") && (s["type"] == "integer" || s["type"] == "number" || s["type"] == "boolean") {
			s = map[string]any{"type": "string"} // encoded as a JSON string
		}
		if desc := f.Tag.Get("desc"); desc != "" {
			s["description"] = desc
		}
		if _, dup := props[name]; !dup {
			*required = append(*required, name)
		}
		props[name] = s
	}
	return nil
}
