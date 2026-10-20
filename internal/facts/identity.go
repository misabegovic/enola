package facts

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// FindingIdentity is the stable identity of a finding across snapshots, as a
// short hex digest of the same key the diff pairs findings on. It is what a
// writer that leaves the binary (a SARIF fingerprint, an annotation a host
// deduplicates between builds) carries, so the identity CI sees is the one
// the verdict was computed with and never a second derivation.
func FindingIdentity(in Insight) string {
	sum := sha256.Sum256([]byte(FindingKey(in)))
	return hex.EncodeToString(sum[:8])
}

// WithIdentities returns v with every finding reachable from it through
// struct fields, pointers and slices carrying its FindingIdentity. What it
// passes through is copied on the way, so v itself, and the snapshot or diff
// it shares slices with, is left as it was.
//
// It walks rather than naming each list because a document grows finding
// lists over time (a verdict carries twelve), and a list added without its
// identity would read to a caller exactly like a finding that has none.
func WithIdentities[T any](v T) T {
	out, ok := stampIdentities(reflect.ValueOf(&v).Elem()).Interface().(T)
	if !ok {
		return v
	}
	return out
}

var insightType = reflect.TypeOf(Insight{})

// reachesInsight caches, per type, whether a finding can sit anywhere below
// it, so a walk never copies what it has nothing to stamp in. Only finished
// answers are stored, so a concurrent writer never reads a half-answered one.
var reachesInsight sync.Map

func holdsInsight(t reflect.Type) bool {
	if cached, ok := reachesInsight.Load(t); ok {
		return cached.(bool)
	}
	holds := holdsInsightVisiting(t, map[reflect.Type]bool{})
	reachesInsight.Store(t, holds)
	return holds
}

// holdsInsightVisiting answers for t without the cache. A type already being
// answered further up reads false, which for a recursive type only means the
// cycle adds no finding its other fields do not.
func holdsInsightVisiting(t reflect.Type, visiting map[reflect.Type]bool) bool {
	if visiting[t] {
		return false
	}
	visiting[t] = true
	switch t.Kind() {
	case reflect.Struct:
		if t == insightType {
			return true
		}
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() && holdsInsightVisiting(t.Field(i).Type, visiting) {
				return true
			}
		}
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return holdsInsightVisiting(t.Elem(), visiting)
	}
	return false
}

func stampIdentities(v reflect.Value) reflect.Value {
	if !holdsInsight(v.Type()) {
		return v
	}
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == insightType {
			in := v.Interface().(Insight)
			in.Identity = FindingIdentity(in)
			return reflect.ValueOf(in)
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if field := out.Field(i); field.CanSet() {
				field.Set(stampIdentities(v.Field(i)))
			}
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(stampIdentities(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(stampIdentities(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(stampIdentities(v.Index(i)))
		}
		return out
	}
	return v
}

// FindingKey identifies an insight so a finding stays the SAME finding across
// snapshots even as its metrics drift or a ranked list re-orders.
//
// Most explainers name their subject (module/symbol/repo/pattern) in the title
// and vary only by counts, so the number-normalized title is the stable identity.
// This is what stops whole-codebase "summary" findings (e.g. the layers pattern,
// whose evidence enumerates every module) from churning resolve+introduce on any
// edit. Cycles are the exception: their title carries only a member count, so two
// distinct cycles would collide — they are keyed on their sorted member modules
// (the evidence), which is also what makes a cycle stay identified as long as its
// membership holds.
func FindingKey(in Insight) string {
	if in.Source == "cycles" {
		return in.Source + "\x00" + EvidenceEntities(in)
	}
	return in.Source + "\x00" + normalizeTitle(in.Title)
}

// EvidenceEntities joins a finding's cited entities (Fact/Symbol/File) in
// sorted order — a stable identity for set-defined findings like cycles.
func EvidenceEntities(in Insight) string {
	var ents []string
	for _, ev := range in.Evidence {
		if e := firstNonEmptyOf(ev.Fact, ev.Symbol, ev.File); e != "" {
			ents = append(ents, e)
		}
	}
	sort.Strings(ents)
	return strings.Join(ents, "\x1f")
}

func firstNonEmptyOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// titleNumber matches the volatile metrics embedded in finding titles (counts,
// ratios, percentages) so they can be stripped for a stable identity.
var titleNumber = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// normalizeTitle removes the volatile numbers from a finding title, leaving the
// stable subject. "Large public surface: x/y exports 67 of 67 symbols (100%)"
// and the same line with different counts collapse to one identity.
func normalizeTitle(s string) string {
	return titleNumber.ReplaceAllString(s, "#")
}
