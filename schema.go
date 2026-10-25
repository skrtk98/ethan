package ethan

import (
	"cmp"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
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
// time.Time are supported, and types implementing encoding.TextMarshaler
// are strings. Maps, interfaces and recursive types are not, since a strict
// schema cannot describe them, and neither are types with their own
// json.Marshaler, such as json.RawMessage, nor field names that embedded
// structs make ambiguous. The length of an array is not in
// the schema, as not every provider supports length constraints.
func SchemaFor[T any]() (json.RawMessage, error) {
	s, err := schemaOf(reflect.TypeFor[T](), nil)
	if err != nil {
		return nil, fmt.Errorf("ethan: schema of %v: %w", reflect.TypeFor[T](), err)
	}
	return json.Marshal(s)
}

var (
	timeType          = reflect.TypeFor[time.Time]()
	numberType        = reflect.TypeFor[json.Number]()
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// implements reports whether t or *t implements iface, as encoding/json
// checks for an addressable value.
func implements(t, iface reflect.Type) bool {
	return t.Implements(iface) || t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(iface)
}

// schemaOf builds the schema of t; seen holds the types being built, to
// reject recursion.
func schemaOf(t reflect.Type, seen []reflect.Type) (map[string]any, error) {
	switch t {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}, nil
	case numberType:
		return map[string]any{"type": "number"}, nil
	}
	if t.Kind() != reflect.Pointer {
		switch {
		case implements(t, marshalerType):
			return nil, fmt.Errorf("type %v encodes itself to JSON", t)
		case implements(t, textMarshalerType):
			return map[string]any{"type": "string"}, nil
		}
	}
	if slices.Contains(seen, t) {
		return nil, fmt.Errorf("recursive type %v", t)
	}
	if t.Name() != "" {
		seen = append(seen, t) // a named slice or pointer type may refer to itself
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
		return nullable(s), nil
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "description": "base64-encoded bytes"}, nil
		}
		items, err := schemaOf(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Struct:
		var all []field
		if err := collectFields(t, 0, seen, &all); err != nil {
			return nil, err
		}
		props := map[string]any{}
		required := []string{}
		for _, f := range all {
			if _, done := props[f.name]; done {
				continue
			}
			win, err := dominant(f.name, all)
			if err == nil {
				err = win.err
			}
			if err != nil {
				return nil, fmt.Errorf("%v: %w", t, err)
			}
			props[f.name] = win.schema
			required = append(required, f.name)
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, nil
	default:
		return nil, fmt.Errorf("unsupported type %v", t)
	}
}

func nullable(s map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
}

// field is a candidate JSON property of a struct.
type field struct {
	name   string
	depth  int  // of embedding
	tagged bool // named by a json tag
	schema map[string]any
	err    error // why there is no schema; it matters only if the field is used
}

// collectFields appends the candidate properties of struct t, at embedding
// depth, to fields, flattening embedded structs as encoding/json does.
func collectFields(t reflect.Type, depth int, seen []reflect.Type, fields *[]field) error {
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" && opts == "" {
			continue
		}
		if !validTagName(name) {
			name = "" // encoding/json ignores it
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				if slices.Contains(seen, ft) {
					return fmt.Errorf("recursive type %v", ft)
				}
				if err := collectFields(ft, depth+1, append(seen, ft), fields); err != nil {
					return err
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		s, err := schemaOf(f.Type, seen)
		if err != nil {
			// A field another one hides does not appear in the JSON.
			*fields = append(*fields, field{name: cmp.Or(name, f.Name), depth: depth, tagged: name != "", err: fmt.Errorf("field %s: %w", f.Name, err)})
			continue
		}
		if quoted(f.Type, opts) {
			s = map[string]any{"type": "string"} // the value inside a JSON string
			if f.Type.Kind() == reflect.Pointer {
				s = nullable(s)
			}
		}
		if desc := f.Tag.Get("desc"); desc != "" {
			s["description"] = desc
		}
		*fields = append(*fields, field{name: cmp.Or(name, f.Name), depth: depth, tagged: name != "", schema: s})
	}
	return nil
}

// dominant picks the field encoding/json uses for name: the shallowest, and
// among those the one named by a tag. Where encoding/json would drop the
// name as ambiguous, it is an error.
func dominant(name string, fields []field) (field, error) {
	var cands []field
	for _, f := range fields {
		if f.name != name {
			continue
		}
		switch {
		case len(cands) == 0 || f.depth < cands[0].depth:
			cands = []field{f}
		case f.depth == cands[0].depth:
			cands = append(cands, f)
		}
	}
	if len(cands) > 1 {
		var tagged []field
		for _, f := range cands {
			if f.tagged {
				tagged = append(tagged, f)
			}
		}
		if len(tagged) != 1 {
			return field{}, fmt.Errorf("ambiguous field %s", name)
		}
		cands = tagged
	}
	return cands[0], nil
}

// quoted reports whether encoding/json writes a field of type t with the
// json options opts as a JSON string holding its value.
func quoted(t reflect.Type, opts string) bool {
	if !slices.Contains(strings.Split(opts, ","), "string") {
		return false
	}
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// validTagName reports whether encoding/json accepts name from a json tag.
func validTagName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}
