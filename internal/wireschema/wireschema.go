// Package wireschema states, as JSON Schema, what encoding/json writes for a Go
// type: the schema is read off the type by reflection, so it cannot come to
// describe a field the type no longer has, and a caller checking a document
// against it checks against the code that wrote it.
//
// It follows encoding/json's own reading of a type rather than a general
// mapping. A field without omitempty is always written, so it is required;
// a nil slice or map without omitempty is written as null, so its type
// admits null; a field promoted through an embedded pointer is written only
// when the pointer is set, so it is not required. A type that writes itself
// (json.Marshaler, encoding.TextMarshaler) has no shape reflection can read,
// and For refuses it by name rather than describing what it does not know.
//
// The schemas do not close objects with additionalProperties: false. A reader
// of a supported format version accepts fields it does not know, since new
// fields do not change the version, and a schema that refused them would
// teach every reader to refuse them too.
package wireschema

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Draft is the JSON Schema dialect every schema here declares.
const Draft = "https://json-schema.org/draft/2020-12/schema"

var (
	jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	timeType      = reflect.TypeOf(time.Time{})
	rawMessage    = reflect.TypeOf(json.RawMessage{})
)

// For returns the schema of what encoding/json writes for a value of type t,
// titled title, with every named struct it reaches under $defs.
func For(t reflect.Type, title string) (map[string]any, error) {
	g := &generator{defs: map[string]any{}, names: map[reflect.Type]string{}, taken: map[string]reflect.Type{}}
	root, err := g.root(t)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{"$schema": Draft, "title": title}
	for k, v := range root {
		doc[k] = v
	}
	if len(g.defs) > 0 {
		doc["$defs"] = g.defs
	}
	return doc, nil
}

type generator struct {
	defs  map[string]any
	names map[reflect.Type]string
	taken map[string]reflect.Type
}

// root inlines the top-level type's own shape instead of referring to it, so
// the document reads as the thing it describes.
func (g *generator) root(t reflect.Type) (map[string]any, error) {
	if t.Kind() == reflect.Struct && !writesItself(t) {
		return g.object(t)
	}
	if t.Kind() == reflect.Slice && t != rawMessage && t.Elem().Kind() != reflect.Uint8 {
		items, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	}
	return g.schema(t)
}

func writesItself(t reflect.Type) bool {
	if t == timeType {
		return false
	}
	for _, c := range []reflect.Type{t, reflect.PointerTo(t)} {
		if c.Implements(jsonMarshaler) || c.Implements(textMarshaler) {
			return true
		}
	}
	return false
}

// schema is the schema of a value of type t as encoding/json writes it,
// where t itself may be null only when its own kind can be.
func (g *generator) schema(t reflect.Type) (map[string]any, error) {
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	if t == rawMessage {
		return map[string]any{}, nil
	}
	if writesItself(t) {
		return nil, fmt.Errorf("%s writes its own JSON; its shape cannot be read from the type", t)
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Pointer:
		inner, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return nullable(inner), nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 && !writesItself(t.Elem()) {
			return map[string]any{"type": []any{"string", "null"}, "contentEncoding": "base64"}, nil
		}
		items, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": []any{"array", "null"}, "items": items}, nil
	case reflect.Array:
		items, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items, "minItems": t.Len(), "maxItems": t.Len()}, nil
	case reflect.Map:
		switch t.Key().Kind() {
		case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		default:
			if !t.Key().Implements(textMarshaler) {
				return nil, fmt.Errorf("%s has keys encoding/json cannot write", t)
			}
		}
		values, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": []any{"object", "null"}, "additionalProperties": values}, nil
	case reflect.Struct:
		if t.Name() == "" {
			return g.object(t)
		}
		name, err := g.define(t)
		if err != nil {
			return nil, err
		}
		return map[string]any{"$ref": "#/$defs/" + name}, nil
	}
	return nil, fmt.Errorf("%s is a %s, which encoding/json cannot write", t, t.Kind())
}

// define records a named struct under $defs once and returns its name there.
func (g *generator) define(t reflect.Type) (string, error) {
	if name, ok := g.names[t]; ok {
		return name, nil
	}
	name := defName(t)
	for n := 2; g.taken[name] != nil; n++ {
		name = fmt.Sprintf("%s%d", defName(t), n)
	}
	g.names[t] = name
	g.taken[name] = t
	g.defs[name] = map[string]any{}
	obj, err := g.object(t)
	if err != nil {
		return "", err
	}
	g.defs[name] = obj
	return name, nil
}

func defName(t reflect.Type) string {
	pkg := t.PkgPath()
	if i := strings.LastIndexByte(pkg, '/'); i >= 0 {
		pkg = pkg[i+1:]
	}
	if pkg == "" {
		return t.Name()
	}
	return pkg + "." + t.Name()
}

func nullable(s map[string]any) map[string]any {
	if ref, ok := s["$ref"]; ok && len(s) == 1 {
		return map[string]any{"anyOf": []any{map[string]any{"$ref": ref}, map[string]any{"type": "null"}}}
	}
	out := map[string]any{}
	for k, v := range s {
		out[k] = v
	}
	switch typ := s["type"].(type) {
	case string:
		out["type"] = []any{typ, "null"}
	case []any:
		for _, x := range typ {
			if x == "null" {
				return out
			}
		}
		out["type"] = append(append([]any{}, typ...), "null")
	case nil:
		// An empty schema already admits null.
	}
	return out
}

// field is one key encoding/json writes for a struct.
type field struct {
	name      string
	typ       reflect.Type
	depth     int
	tagged    bool
	omitEmpty bool
	asString  bool
	// optional marks a field promoted through an embedded pointer, which is
	// written only when the pointer is set.
	optional bool
	index    []int
}

// object is the schema of a struct as encoding/json writes it.
func (g *generator) object(t reflect.Type) (map[string]any, error) {
	props := map[string]any{}
	var required []string
	for _, f := range jsonFields(t) {
		s, err := g.schema(f.typ)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t, f.name, err)
		}
		if f.asString {
			s = map[string]any{"type": "string"}
		}
		if f.omitEmpty {
			s = withoutNull(s, f.typ)
		}
		props[f.name] = s
		if !f.omitEmpty && !f.optional {
			required = append(required, f.name)
		}
	}
	obj := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		obj["required"] = required
	}
	return obj, nil
}

// withoutNull narrows a field encoding/json omits when empty: a nil slice,
// map or pointer is never written as null under omitempty, it is left out.
func withoutNull(s map[string]any, t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer:
	default:
		return s
	}
	if alts, ok := s["anyOf"].([]any); ok && len(alts) == 2 {
		if first, ok := alts[0].(map[string]any); ok {
			return first
		}
	}
	typ, ok := s["type"].([]any)
	if !ok {
		return s
	}
	var kept []any
	for _, x := range typ {
		if x != "null" {
			kept = append(kept, x)
		}
	}
	out := map[string]any{}
	for k, v := range s {
		out[k] = v
	}
	if len(kept) == 1 {
		out["type"] = kept[0]
	} else {
		out["type"] = kept
	}
	return out
}

// jsonFields lists the keys encoding/json writes for t, by its rules:
// embedded structs promote their fields, a shallower field hides a deeper
// one of the same name, a tagged one wins a tie, and a remaining tie writes
// neither.
func jsonFields(t reflect.Type) []field {
	var all []field
	var walk func(t reflect.Type, depth int, index []int, optional bool, visited map[reflect.Type]bool)
	walk = func(t reflect.Type, depth int, index []int, optional bool, visited map[reflect.Type]bool) {
		if visited[t] {
			return
		}
		visited[t] = true
		defer delete(visited, t)
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			tag := sf.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			ft := sf.Type
			if sf.Anonymous {
				et := ft
				ptr := false
				if et.Kind() == reflect.Pointer {
					et, ptr = et.Elem(), true
				}
				if !sf.IsExported() && et.Kind() != reflect.Struct {
					continue
				}
				if name == "" && et.Kind() == reflect.Struct {
					walk(et, depth+1, append(append([]int{}, index...), i), optional || ptr, visited)
					continue
				}
			} else if !sf.IsExported() {
				continue
			}
			tagged := name != ""
			if name == "" {
				name = sf.Name
			}
			all = append(all, field{
				name:      name,
				typ:       ft,
				depth:     depth,
				tagged:    tagged,
				omitEmpty: hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero"),
				asString:  hasOpt(opts, "string") && isScalar(ft),
				optional:  optional,
				index:     append(append([]int{}, index...), i),
			})
		}
	}
	walk(t, 0, nil, false, map[reflect.Type]bool{})

	byName := map[string][]field{}
	var order []string
	for _, f := range all {
		if _, seen := byName[f.name]; !seen {
			order = append(order, f.name)
		}
		byName[f.name] = append(byName[f.name], f)
	}
	var out []field
	for _, name := range order {
		if f, ok := dominant(byName[name]); ok {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return lessIndex(out[i].index, out[j].index) })
	return out
}

func dominant(fields []field) (field, bool) {
	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].depth != fields[j].depth {
			return fields[i].depth < fields[j].depth
		}
		return fields[i].tagged && !fields[j].tagged
	})
	if len(fields) > 1 && fields[0].depth == fields[1].depth && fields[0].tagged == fields[1].tagged {
		return field{}, false
	}
	return fields[0], true
}

func lessIndex(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func isScalar(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}
