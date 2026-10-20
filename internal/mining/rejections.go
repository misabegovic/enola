package mining

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// RejectionsFileName is where a repository keeps the candidates a person
// rejected, beside enola/constraints/ and outside it, so the constraints
// loader never reads a rejection as a declaration. mine reads it and never
// writes it: a rejection is a person's decision, written by that person.
const RejectionsFileName = "enola/mine-rejections.yaml"

// Rejection is one rejected candidate: its identity as mine printed it, who
// rejected it, when, and why. The fields are an exemption's, owner, since and
// because, so the two decisions read alike.
type Rejection struct {
	Identity string `yaml:"identity" json:"identity"`
	Owner    string `yaml:"owner" json:"owner"`
	Since    string `yaml:"since" json:"since"`
	Because  string `yaml:"because" json:"because"`
}

type rejectionsFile struct {
	Rejections []Rejection `yaml:"rejections"`
}

// LoadRejections reads the rejections file at path, resolved against repoPath
// when relative. An absent file is no rejections. A file that is present and
// does not read whole is an error, never a partial read: a rejection silently
// dropped would put the candidate back in front of the person who said no.
func LoadRejections(repoPath, path string) ([]Rejection, error) {
	if path == "" {
		path = RejectionsFileName
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoPath, filepath.FromSlash(path))
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var file rejectionsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var problems []string
	seen := map[string]int{}
	for i, r := range file.Rejections {
		at := fmt.Sprintf("rejections[%d]", i)
		if r.Identity == "" {
			problems = append(problems, at+": missing identity")
		} else if prior, dup := seen[r.Identity]; dup {
			problems = append(problems, fmt.Sprintf("%s: identity %q is already rejected at rejections[%d]", at, r.Identity, prior))
		} else {
			seen[r.Identity] = i
		}
		if r.Owner == "" {
			problems = append(problems, at+": missing owner (who rejected it)")
		}
		if _, err := time.Parse("2006-01-02", r.Since); err != nil {
			problems = append(problems, fmt.Sprintf("%s: since %q is not a date written YYYY-MM-DD", at, r.Since))
		}
		if r.Because == "" {
			problems = append(problems, at+": missing because (why it was rejected)")
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %d problem(s): %s", path, len(problems), joinProblems(problems))
	}
	sort.Slice(file.Rejections, func(i, j int) bool { return file.Rejections[i].Identity < file.Rejections[j].Identity })
	return file.Rejections, nil
}

func joinProblems(problems []string) string {
	var b bytes.Buffer
	for i, p := range problems {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p)
	}
	return b.String()
}

// RejectedCandidate is a candidate mine found and did not propose, because a
// person rejected it.
type RejectedCandidate struct {
	Candidate
	Rejection Rejection `json:"rejection"`
}

// applyRejections moves every candidate a rejection names out of the list,
// and returns the rejections that named no candidate the run produced.
func applyRejections(candidates []Candidate, rejections []Rejection, alsoProduced ...[]Candidate) (kept []Candidate, rejected []RejectedCandidate, stale []Rejection) {
	byID := map[string]Rejection{}
	for _, r := range rejections {
		byID[r.Identity] = r
	}
	used := map[string]bool{}
	for _, c := range candidates {
		if r, ok := byID[c.Identity]; ok {
			rejected = append(rejected, RejectedCandidate{Candidate: c, Rejection: r})
			used[c.Identity] = true
			continue
		}
		kept = append(kept, c)
	}
	for _, list := range alsoProduced {
		for _, c := range list {
			if _, ok := byID[c.Identity]; ok {
				used[c.Identity] = true
			}
		}
	}
	for _, r := range rejections {
		if !used[r.Identity] {
			stale = append(stale, r)
		}
	}
	return kept, rejected, stale
}
