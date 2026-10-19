package ethan

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// validate checks v, decoded with json.Decoder.UseNumber, against s, a
// schema as schemaOf builds them. path locates v in error messages.
func validate(s map[string]any, v any, path string) error {
	if branches, ok := s["anyOf"].([]any); ok {
		var errs []string
		for _, b := range branches {
			err := validate(b.(map[string]any), v, path)
			if err == nil {
				return nil
			}
			errs = append(errs, err.Error())
		}
		return fmt.Errorf("%s matches none of: %s", path, strings.Join(errs, "; "))
	}
	switch typ := s["type"]; typ {
	case "null":
		if v != nil {
			return fmt.Errorf("%s: want null", path)
		}
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s: want a string", path)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: want a boolean", path)
		}
	case "integer", "number":
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: want a number", path)
		}
		// encoding/json decodes only plain digits into Go integers: 1.0
		// and 1e0 are rejected here rather than when decoding.
		if typ == "integer" && strings.ContainsAny(string(n), ".eE") {
			return fmt.Errorf("%s: want an integer written without a fraction or exponent", path)
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: want an array", path)
		}
		for i, it := range items {
			if err := validate(s["items"].(map[string]any), it, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object", path)
		}
		props := s["properties"].(map[string]any)
		for _, name := range s["required"].([]string) {
			if _, ok := obj[name]; !ok {
				return fmt.Errorf("%s: missing %s", path, name)
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			ps, ok := props[k]
			if !ok {
				return fmt.Errorf("%s: unknown property %s", path, k)
			}
			if err := validate(ps.(map[string]any), obj[k], path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}
