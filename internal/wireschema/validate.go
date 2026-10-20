package wireschema

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Validate checks a decoded JSON document against a schema For wrote, and
// returns every place the document departs from it. It reads the part of
// JSON Schema For writes and nothing more: type, properties, required,
// items, additionalProperties, anyOf, and references into $defs.
//
// strict also reports a property the schema does not name. A reader does not
// need that, and must not ask for it; it is how a test proves a schema
// describes everything the encoder writes.
func Validate(schema map[string]any, doc any, strict bool) []string {
	v := validator{root: schema, strict: strict}
	v.check("$", schema, doc)
	sort.Strings(v.errs)
	return v.errs
}

// ValidateJSON is Validate over encoded bytes.
func ValidateJSON(schema map[string]any, data []byte, strict bool) []string {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return []string{"not JSON: " + err.Error()}
	}
	return Validate(schema, doc, strict)
}

type validator struct {
	root   map[string]any
	strict bool
	errs   []string
}

func (v *validator) fail(path, format string, args ...any) {
	v.errs = append(v.errs, path+": "+fmt.Sprintf(format, args...))
}

func (v *validator) resolve(s map[string]any) map[string]any {
	for {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		name, found := strings.CutPrefix(ref, "#/$defs/")
		defs, _ := v.root["$defs"].(map[string]any)
		next, _ := defs[name].(map[string]any)
		if !found || next == nil {
			v.fail(ref, "reference to nothing")
			return map[string]any{}
		}
		s = next
	}
}

func (v *validator) check(path string, s map[string]any, doc any) {
	s = v.resolve(s)
	if alts, ok := s["anyOf"].([]any); ok {
		for _, alt := range alts {
			a, _ := alt.(map[string]any)
			sub := validator{root: v.root, strict: v.strict}
			sub.check(path, a, doc)
			if len(sub.errs) == 0 {
				return
			}
		}
		v.fail(path, "matches no alternative")
		return
	}
	if t, ok := s["type"]; ok && !typeAdmits(t, doc) {
		v.fail(path, "%s is not %v", kindOf(doc), t)
		return
	}
	switch x := doc.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		for _, r := range asStrings(s["required"]) {
			if _, present := x[r]; !present {
				v.fail(path, "missing required %q", r)
			}
		}
		extra, _ := s["additionalProperties"].(map[string]any)
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p, ok := props[k].(map[string]any); ok {
				v.check(path+"."+k, p, x[k])
				continue
			}
			if extra != nil {
				v.check(path+"."+k, extra, x[k])
				continue
			}
			if v.strict && props != nil {
				v.fail(path, "property %q is not in the schema", k)
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, item := range x {
				v.check(fmt.Sprintf("%s[%d]", path, i), items, item)
			}
		}
	}
}

func typeAdmits(t any, doc any) bool {
	switch tt := t.(type) {
	case string:
		return admits(tt, doc)
	case []any:
		for _, x := range tt {
			if s, ok := x.(string); ok && admits(s, doc) {
				return true
			}
		}
	case []string:
		for _, s := range tt {
			if admits(s, doc) {
				return true
			}
		}
	}
	return false
}

func admits(t string, doc any) bool {
	switch t {
	case "null":
		return doc == nil
	case "boolean":
		_, ok := doc.(bool)
		return ok
	case "string":
		_, ok := doc.(string)
		return ok
	case "number":
		_, ok := doc.(float64)
		return ok
	case "integer":
		f, ok := doc.(float64)
		return ok && f == math.Trunc(f)
	case "array":
		_, ok := doc.([]any)
		return ok
	case "object":
		_, ok := doc.(map[string]any)
		return ok
	}
	return false
}

func kindOf(doc any) string {
	switch doc.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case float64:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", doc)
}

func asStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
