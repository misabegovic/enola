package intent

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnknownKey is one key a declaration file carries that no field of the entry
// holding it reads. The decoder drops such a key without a word, so a rule
// someone wrote with a misspelt or invented key reads as the rule without it;
// naming the key, its file and its line is the only way the author learns the
// law they wrote is not the law that runs.
//
// It is a warning and never a problem. Hand-written declarations exist with
// keys this build never read, and refusing them would fail a declaration
// that has always compiled.
type UnknownKey struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// Rule or Component names the entry the key sits in, by id or name; both
	// are empty for a key at the top of the file.
	Rule      string `json:"rule,omitempty"`
	Component string `json:"component,omitempty"`
	Key       string `json:"key"`
}

// String renders the warning as file:line, the entry and the key, the shape a
// compiler warning takes so an editor can jump to it.
func (u UnknownKey) String() string {
	var where string
	switch {
	case u.Rule != "":
		where = "rule " + u.Rule + ": "
	case u.Component != "":
		where = "component " + u.Component + ": "
	}
	return u.File + ":" + strconv.Itoa(u.Line) + ": " + where + "key \"" + u.Key + "\" is read by nothing; it has no effect"
}

// RuleKeys is every key a rule may carry, read from ConstraintRule's own
// field tags so the list and the decoder cannot disagree about a key.
func RuleKeys() []string { return yamlKeys(reflect.TypeOf(ConstraintRule{})) }

// ComponentKeys is every key a component may carry, read the same way.
func ComponentKeys() []string { return yamlKeys(reflect.TypeOf(ConstraintComponent{})) }

func yamlKeys(t reflect.Type) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func keySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// unknownKeys lists the keys of one declaration file the decoder maps to no
// field: at the top of the file against top, and inside every entry of its
// rules and components lists. Nested values (an exemption, a predicate) are
// left to their own decoders. A file that does not parse yields nothing here,
// since its parse error is already the problem reported for it.
func unknownKeys(data []byte, file string, top reflect.Type) []UnknownKey {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	topKeys := keySet(yamlKeys(top))
	ruleKeys := keySet(RuleKeys())
	componentKeys := keySet(ComponentKeys())
	var out []UnknownKey
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if !topKeys[key.Value] {
			out = append(out, UnknownKey{File: file, Line: key.Line, Key: key.Value})
			continue
		}
		var known map[string]bool
		var nameKey string
		switch key.Value {
		case "rules":
			known, nameKey = ruleKeys, "id"
		case "components":
			known, nameKey = componentKeys, "name"
		default:
			continue
		}
		if value.Kind != yaml.SequenceNode {
			continue
		}
		for _, entry := range value.Content {
			if entry.Kind != yaml.MappingNode {
				continue
			}
			name := ""
			for j := 0; j+1 < len(entry.Content); j += 2 {
				if entry.Content[j].Value == nameKey {
					name = entry.Content[j+1].Value
				}
			}
			for j := 0; j+1 < len(entry.Content); j += 2 {
				k := entry.Content[j]
				if known[k.Value] {
					continue
				}
				u := UnknownKey{File: file, Line: k.Line, Key: k.Value}
				if key.Value == "rules" {
					u.Rule = name
				} else {
					u.Component = name
				}
				out = append(out, u)
			}
		}
	}
	return out
}

// DeclarationUnknownKeys lists the keys of the repository's declaration file
// that nothing reads, cited against file.
func DeclarationUnknownKeys(data []byte, file string) []UnknownKey {
	return unknownKeys(data, file, reflect.TypeOf(Declaration{}))
}

// EntryLines reads a declaration file for the line each entry of its
// components and rules lists starts on, by list and position: the location a
// validation problem names as components[i] or rules[i].
func EntryLines(data []byte) map[string][]int {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	out := map[string][]int{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if (key.Value != "rules" && key.Value != "components") || value.Kind != yaml.SequenceNode {
			continue
		}
		for _, entry := range value.Content {
			out[key.Value] = append(out[key.Value], entry.Line)
		}
	}
	return out
}
