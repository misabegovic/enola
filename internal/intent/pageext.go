package intent

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// PageExtensions is the comma-separated list of page extensions a build reads
// beside markdown, set by the linker:
//
//	-X github.com/enola-labs/enola/internal/intent.PageExtensions=.ext
//
// The source leaves it empty, so a build reads markdown alone unless its recipe
// says otherwise. It is a build setting rather than a configuration key because
// the engine is also run over a bare directory, where there is no configuration
// to carry a key, and a build reaches every consumer it is installed for.
var PageExtensions = ""

// markdownPageExtension is read by every build and is never listed: a list
// that could name it would give the default two spellings.
const markdownPageExtension = ".md"

type pageExtensionList struct {
	listed []string
	err    error
}

// buildPageExtensions is PageExtensions read once. The linker writes the
// string before any initializer runs, so what is read here is the stamp.
var buildPageExtensions = readPageExtensionList(PageExtensions)

func readPageExtensionList(list string) pageExtensionList {
	listed, err := ReadPageExtensions(list)
	return pageExtensionList{listed: listed, err: err}
}

// ReadPageExtensions reads a comma-separated list of page extensions into a
// sorted set without repeats, so the order a list is written in never reaches
// a snapshot. An entry is a dot and a name with no dot, path separator or
// space in it, and never .md; any other entry refuses the whole list by name,
// because which pages a build reads must not depend on how a typo parses.
func ReadPageExtensions(list string) ([]string, error) {
	if list == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, entry := range strings.Split(list, ",") {
		if entry == markdownPageExtension {
			return nil, fmt.Errorf("page extensions %q: page extension %q is read by every build and is never listed", list, entry)
		}
		if !isPageExtension(entry) {
			return nil, fmt.Errorf("page extensions %q: page extension %q is not a dot followed by a name without a dot, path separator or space", list, entry)
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	sort.Strings(out)
	return out, nil
}

func isPageExtension(entry string) bool {
	if len(entry) < 2 || entry[0] != '.' {
		return false
	}
	for _, r := range entry[1:] {
		if r == '.' || r == '/' || r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// PageExtensionsError is the error refusing the build's list, or nil. A
// refused list is read as empty, so the error is the only thing that tells a
// broken stamp from a build that lists nothing, and the extractor reading
// pages fails the snapshot on it.
func PageExtensionsError() error { return buildPageExtensions.err }

// PageExtensionsRead names every extension this build reads a page from,
// markdown first.
func PageExtensionsRead() []string {
	return append([]string{markdownPageExtension}, buildPageExtensions.listed...)
}

// IsPagePath reports whether path names a page this build reads. It is the one
// answer to that question: the page extractor's gates, a document's title and
// the relation rule all ask it, so no reader can come to read a set of
// extensions the others do not.
func IsPagePath(path string) bool { return pageExtensionOf(path) != "" }

// TrimPageExtension drops the page extension name ends in; a name that is no
// page path comes back unchanged.
func TrimPageExtension(name string) string {
	return strings.TrimSuffix(name, pageExtensionOf(name))
}

func pageExtensionOf(path string) string {
	if strings.HasSuffix(path, markdownPageExtension) {
		return markdownPageExtension
	}
	for _, ext := range buildPageExtensions.listed {
		if strings.HasSuffix(path, ext) {
			return ext
		}
	}
	return ""
}

// UsePageExtensions reads list in place of the build's page extensions until
// the returned function puts the build's back. Tests use it because they
// cannot pass a linker flag; a build's list is fixed when it is linked.
func UsePageExtensions(list string) (restore func()) {
	previous := buildPageExtensions
	buildPageExtensions = readPageExtensionList(list)
	return func() { buildPageExtensions = previous }
}
