package intent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The source reads markdown alone. A build that stamps nothing must behave as
// every build did before the list existed, so the default is pinned here and
// not left to whatever a test before this one set.
func TestPageExtensions_TheSourceReadsMarkdownAlone(t *testing.T) {
	if PageExtensions != "" {
		t.Fatalf("PageExtensions = %q in the source, want empty: a build extends it, the source never does", PageExtensions)
	}
	if err := PageExtensionsError(); err != nil {
		t.Fatalf("the empty list is refused: %v", err)
	}
	if got := PageExtensionsRead(); !reflect.DeepEqual(got, []string{".md"}) {
		t.Fatalf("PageExtensionsRead() = %v, want [.md]", got)
	}
	for path, want := range map[string]bool{
		"wiki/a.md":   true,
		"a.md":        true,
		"wiki/b.leaf": false,
		"wiki/a.mdx":  false,
		"wiki/md":     false,
	} {
		if got := IsPagePath(path); got != want {
			t.Errorf("IsPagePath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestPageExtensions_AStampedListIsReadBesideMarkdown(t *testing.T) {
	defer UsePageExtensions(".leaf")()
	if err := PageExtensionsError(); err != nil {
		t.Fatalf("a well-formed list is refused: %v", err)
	}
	if got := PageExtensionsRead(); !reflect.DeepEqual(got, []string{".md", ".leaf"}) {
		t.Fatalf("PageExtensionsRead() = %v, want [.md .leaf]", got)
	}
	for path, want := range map[string]bool{
		"wiki/a.md":        true,
		"wiki/b.leaf":      true,
		"b.leaf":           true,
		"wiki/b.leafy":     false,
		"wiki/b.leaf/c.rb": false,
		"wiki/bleaf":       false,
	} {
		if got := IsPagePath(path); got != want {
			t.Errorf("IsPagePath(%q) = %v, want %v", path, got, want)
		}
	}
}

// The seam restores what it replaced, so one test's list never reaches the next.
func TestPageExtensions_TheSeamRestoresTheBuildsList(t *testing.T) {
	restore := UsePageExtensions(".leaf,.bark")
	if !IsPagePath("x.bark") {
		t.Fatal("the seam's list is not read")
	}
	restore()
	if IsPagePath("x.bark") || IsPagePath("x.leaf") {
		t.Fatal("the seam's list outlived its restore")
	}
	if got := PageExtensionsRead(); !reflect.DeepEqual(got, []string{".md"}) {
		t.Fatalf("after restore PageExtensionsRead() = %v, want [.md]", got)
	}
}

// The order a list is written in, and a repeat in it, never reach a snapshot.
func TestPageExtensions_OrderAndRepeatsDoNotMatter(t *testing.T) {
	want := []string{".bark", ".leaf"}
	for _, list := range []string{".leaf,.bark", ".bark,.leaf", ".leaf,.bark,.leaf", ".bark,.bark,.leaf"} {
		got, err := ReadPageExtensions(list)
		if err != nil {
			t.Fatalf("ReadPageExtensions(%q): %v", list, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ReadPageExtensions(%q) = %v, want %v", list, got, want)
		}
	}
	if got, err := ReadPageExtensions(""); err != nil || len(got) != 0 {
		t.Fatalf("ReadPageExtensions(\"\") = %v, %v, want nothing and no error", got, err)
	}
}

// A stamp that is not a list of extensions is refused by name, never read as a
// narrower list: which pages a build reads must not depend on how a typo parses.
func TestPageExtensions_AMalformedEntryIsRefusedByName(t *testing.T) {
	for list, entry := range map[string]string{
		"leaf":        "leaf",
		".":           ".",
		".leaf,":      "",
		",.leaf":      "",
		".leaf,,bark": "",
		"./leaf":      "./leaf",
		".le/af":      ".le/af",
		`.le\af`:      `.le\af`,
		".le af":      ".le af",
		" .leaf":      " .leaf",
		".leaf.bak":   ".leaf.bak",
		"..leaf":      "..leaf",
		".md":         ".md",
		".leaf,.md":   ".md",
	} {
		_, err := ReadPageExtensions(list)
		if err == nil {
			t.Errorf("ReadPageExtensions(%q) read a malformed list", list)
			continue
		}
		if !strings.Contains(err.Error(), "page extension") || !strings.Contains(err.Error(), fmt.Sprintf("%q", entry)) {
			t.Errorf("ReadPageExtensions(%q) = %v, want the entry %q named", list, err, entry)
		}
	}
}

// A build whose stamp is refused says so wherever the list is asked for, and
// reads markdown alone meanwhile: the error is the loud part, the fallback is
// what keeps a refused list from reading as a narrower one.
func TestPageExtensions_ARefusedStampReadsMarkdownAndReportsTheError(t *testing.T) {
	defer UsePageExtensions("leaf")()
	if err := PageExtensionsError(); err == nil || !strings.Contains(err.Error(), `"leaf"`) {
		t.Fatalf("PageExtensionsError() = %v, want the entry named", err)
	}
	if IsPagePath("x.leaf") || !IsPagePath("x.md") {
		t.Fatal("a refused list must leave markdown alone read")
	}
}

func TestPageExtensions_TrimPageExtension(t *testing.T) {
	defer UsePageExtensions(".leaf")()
	for name, want := range map[string]string{
		"b.leaf":    "b",
		"b.md":      "b",
		"b.spec.md": "b.spec",
		"b.md.leaf": "b.md",
		"b.leafy":   "b.leafy",
		"README":    "README",
		"notes.txt": "notes.txt",
	} {
		if got := TrimPageExtension(name); got != want {
			t.Errorf("TrimPageExtension(%q) = %q, want %q", name, got, want)
		}
	}
}

const relationTo = "---\nenola_intent:\n  page:\n    type: decision\n    relations:\n      - {rel: depends-on, to: %s}\n---\nbody\n"

// With the list empty, a relation to a page of another extension fails as it
// always has, and the message names the extensions the build reads, so an
// upstream build's refusal says why rather than only that.
func TestPageRelation_ToAnUnreadExtensionFailsNamingWhatIsRead(t *testing.T) {
	_, err := ParsePage([]byte(strings.Replace(relationTo, "%s", "wiki/b.leaf", 1)))
	if err == nil {
		t.Fatal("a relation to an extension the build does not read must fail")
	}
	for _, want := range []string{`page.relations[0]: to "wiki/b.leaf"`, "(.md)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
	if _, err := ParsePage([]byte(strings.Replace(relationTo, "%s", "wiki/b.md", 1))); err != nil {
		t.Fatalf("a relation to a markdown page fails: %v", err)
	}
}

func TestPageRelation_ToAListedExtensionBinds(t *testing.T) {
	defer UsePageExtensions(".leaf")()
	p, err := ParsePage([]byte(strings.Replace(relationTo, "%s", "wiki/b.leaf", 1)))
	if err != nil {
		t.Fatalf("a relation to a listed page is refused: %v", err)
	}
	if got := p.Page.Relations[0].To; got != "wiki/b.leaf" {
		t.Fatalf("relation target = %q", got)
	}
	_, err = ParsePage([]byte(strings.Replace(relationTo, "%s", "wiki/b.bark", 1)))
	if err == nil || !strings.Contains(err.Error(), "(.md, .leaf)") {
		t.Fatalf("an unlisted extension must fail naming both read extensions, got %v", err)
	}
}
