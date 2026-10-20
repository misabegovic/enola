package server

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// member is one repository of the loaded graph: the label its facts carry and
// the absolute path of its checkout.
type member struct {
	label string
	path  string
}

// members lists the repositories of the loaded graph, in the order the config
// lists them when it lists every one, and by label otherwise. With nothing
// loaded it lists the config's own, so a server over an engine a command has
// not yet filled answers for what the command was pointed at.
func (s *Server) members() []member {
	loaded := s.eng.RepoPaths()
	var configured []member
	if paths, err := s.cfg.RepoPaths(); err == nil {
		for _, p := range paths {
			if abs, err := filepath.Abs(p); err == nil {
				configured = append(configured, member{label: filepath.Base(abs), path: abs})
			}
		}
	}
	if s.eng.Snapshot() == nil || s.eng.Store().Count() == 0 {
		return configured
	}
	if len(loaded) == 0 {
		if p := s.currentRepoPath(); p != "" {
			return []member{{label: filepath.Base(p), path: p}}
		}
		return nil
	}
	labelOf := make(map[string]string, len(loaded))
	for label, path := range loaded {
		labelOf[path] = label
	}
	ordered := make([]member, 0, len(configured))
	for _, c := range configured {
		if label, ok := labelOf[c.path]; ok {
			ordered = append(ordered, member{label: label, path: c.path})
		}
	}
	if len(ordered) == len(loaded) {
		return ordered
	}
	out := make([]member, 0, len(loaded))
	for label, path := range loaded {
		out = append(out, member{label: label, path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

// memberPaths is members as paths, in the same order.
func memberPaths(ms []member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.path
	}
	return out
}

func (m member) named(repo string) bool {
	if repo == m.label || repo == filepath.Base(m.path) {
		return true
	}
	abs, err := filepath.Abs(repo)
	return err == nil && filepath.IsAbs(repo) && abs == m.path
}

func memberLabels(ms []member) string {
	labels := make([]string, len(ms))
	for i, m := range ms {
		labels[i] = m.label
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// anchor is the repository a tool that answers for one repository answers for.
//
// With one repository loaded it is that one, as it always was: repo may name
// it and changes nothing, and a repo naming another is refused rather than
// ignored. In a union, repo names the member; with no repo, a path whose first
// segment is a member's label names it, and every path given must name the
// same one; anything else is refused naming the members. paths is nil for a
// tool that takes none, whose refusal then asks for repo alone. The member loaded
// last is never assumed: it answered plan_check about one member's file with
// another member's contract, confidently.
//
// The refusal is the returned string, empty when a member was chosen.
func (s *Server) anchor(tool, repo string, paths []string) (member, string) {
	return s.anchorOn(tool, repo, paths, false)
}

// answerAnchor is anchor for the tools a command calls for its answer. With one
// repository it answers for the checkout the graph was restored for, where
// anchor keeps the path the snapshot recorded, as the older tools always have:
// a snapshot copied along with its checkout records where it was generated,
// and the suppressions, declarations and tree these tools read are the ones
// beside it now.
func (s *Server) answerAnchor(tool, repo string) (member, string) {
	return s.anchorOn(tool, repo, nil, true)
}

func (s *Server) anchorOn(tool, repo string, paths []string, checkout bool) (member, string) {
	ms := s.members()
	if len(ms) <= 1 {
		one := member{path: s.currentRepoPath()}
		one.label = filepath.Base(one.path)
		if len(ms) == 1 {
			one.label = ms[0].label
			if checkout {
				one.path = ms[0].path
			}
		}
		if repo != "" && !one.named(repo) && !(len(ms) == 1 && ms[0].named(repo)) {
			return member{}, fmt.Sprintf("%s: repo %q is not loaded here; this snapshot holds %s.", tool, repo, one.label)
		}
		return one, ""
	}
	if repo != "" {
		for _, m := range ms {
			if m.named(repo) {
				return m, ""
			}
		}
		return member{}, fmt.Sprintf("%s: repo %q is not a repository of this snapshot, which holds %d: %s. Pass one of them as repo.",
			tool, repo, len(ms), memberLabels(ms))
	}
	if len(paths) == 0 {
		return member{}, fmt.Sprintf("%s answers for one repository, and this snapshot holds %d: %s. Pass repo as one of them.",
			tool, len(ms), memberLabels(ms))
	}
	var chosen *member
	for _, p := range paths {
		first, _, _ := strings.Cut(filepath.ToSlash(p), "/")
		var hit *member
		for i := range ms {
			if ms[i].label == first {
				hit = &ms[i]
				break
			}
		}
		if hit == nil {
			return member{}, fmt.Sprintf("%s: path %q names no repository. Start it with its repository's name (%s/...) or pass repo; "+
				"this snapshot holds %d: %s.", tool, p, ms[0].label, len(ms), memberLabels(ms))
		}
		if chosen != nil && chosen.label != hit.label {
			return member{}, fmt.Sprintf("%s: the targets are in %s and %s, and it answers for one repository, so plan each separately; "+
				"this snapshot holds %d: %s.", tool, chosen.label, hit.label, len(ms), memberLabels(ms))
		}
		chosen = hit
	}
	return *chosen, ""
}

// withinMember turns a path that starts with the member's label into the
// repository-relative path the member's own tree and declarations use. A path
// that does not start with it is returned as it is.
func withinMember(m member, path string) string {
	slashed := filepath.ToSlash(path)
	if rest, ok := strings.CutPrefix(slashed, m.label+"/"); ok && m.label != "" {
		return rest
	}
	return path
}
