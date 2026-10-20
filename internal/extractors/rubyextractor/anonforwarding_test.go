package rubyextractor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ruby "github.com/enola-labs/enola/internal/extractors/rubyextractor/grammar"
	"github.com/enola-labs/enola/internal/facts"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Ruby 3.1's anonymous block forwarding with whitespace after the `&` is the
// construct tree-sitter-ruby v0.23.1 could not read: its scanner took any `&`
// followed by a space or a newline for the binary operator, so `}, &` at the
// end of a line and `&` alone before the closing parenthesis left an error
// node, and its recovery dropped the methods after it. Each fixture is a shape
// measured in the corpus on 2026-10-06 (once-campfire's and writebook's
// helpers, redmine's adapters), with the methods the file declares after it.
var anonymousForwardingFixtures = map[string]struct {
	src     string
	methods []string
}{
	"app/helpers/messages_helper.rb": {
		src: "module MessagesHelper\n" +
			"  def message_area_tag(room, &)\n" +
			"    tag.div id: \"message-area\", data: {\n" +
			"      controller: \"messages\",\n" +
			"      messages_page_url_value: room_messages_url(room)\n" +
			"    }, &\n" +
			"  end\n" +
			"\n" +
			"  def message_tag(message, &)\n" +
			"    tag.div id: dom_id(message), &\n" +
			"  end\n" +
			"\n" +
			"  def message_author_title(author)\n" +
			"    author.name\n" +
			"  end\n" +
			"end\n",
		methods: []string{"MessagesHelper#message_area_tag", "MessagesHelper#message_tag", "MessagesHelper#message_author_title"},
	},
	"app/helpers/leaves_helper.rb": {
		src: "module LeavesHelper\n" +
			"  def leaf_item_tag(leaf, **, &)\n" +
			"    tag.li class: \"arrangement__item\", data: {\n" +
			"      id: leaf.id\n" +
			"    }, **, &\n" +
			"  end\n" +
			"\n" +
			"  def leafable_edit_path(leaf)\n" +
			"    edit_polymorphic_path(leaf)\n" +
			"  end\n" +
			"end\n",
		methods: []string{"LeavesHelper#leaf_item_tag", "LeavesHelper#leafable_edit_path"},
	},
	"app/helpers/forms_helper.rb": {
		src: "module FormsHelper\n" +
			"  def auto_submit_form_with(**attributes, &)\n" +
			"    form_with **attributes, data: { controller: \"auto-submit\" }, &\n" +
			"  end\n" +
			"\n" +
			"  def submit_label\n" +
			"  end\n" +
			"end\n",
		methods: []string{"FormsHelper#auto_submit_form_with", "FormsHelper#submit_label"},
	},
	"lib/adapters/git_adapter.rb": {
		src: "class GitAdapter\n" +
			"  def git_cmd(args, options = {}, &)\n" +
			"    shellout(\n" +
			"      args,\n" +
			"      options,\n" +
			"      &\n" +
			"    )\n" +
			"  end\n" +
			"\n" +
			"  def branches\n" +
			"  end\n" +
			"end\n",
		methods: []string{"GitAdapter#git_cmd", "GitAdapter#branches"},
	},
}

func TestExtract_AnonymousBlockForwardingReadsTheWholeFile(t *testing.T) {
	repo := t.TempDir()
	var rel []string
	for name, fixture := range anonymousForwardingFixtures {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fixture.src), 0o644); err != nil {
			t.Fatal(err)
		}
		rel = append(rel, name)
	}
	out, err := New().Extract(context.Background(), repo, rel)
	if err != nil {
		t.Fatal(err)
	}
	byName := symbolsByName(out)
	for name, fixture := range anonymousForwardingFixtures {
		for _, method := range fixture.methods {
			if _, ok := byName[method]; !ok {
				t.Errorf("%s: %s is missing, the reader stopped at the forwarded block", name, method)
			}
		}
	}
	for _, f := range out {
		if f.Kind == facts.KindExtraction && f.Name == "ruby:parse" {
			if got := f.PropAny(facts.PropParsedWithErrors); got != nil {
				t.Errorf("%s = %v, want every file read whole", facts.PropParsedWithErrors, got)
			}
		}
	}
}

// The patch decides between the block argument and the binary operator only
// where both could follow; the binary `&` keeps its meaning wherever a
// left operand stands before it, at the end of a line included.
func TestGrammar_AmpersandIsBinaryAfterAnOperandAndABlockArgumentAfterAComma(t *testing.T) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(ruby.Language())); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ src, want, refuse string }{
		{"flags & MASK\n", "(binary left: (identifier) right: (constant))", "block_argument"},
		{"x = flags &\n  MASK\n", "(binary left: (identifier) right: (constant))", "block_argument"},
		{"foo 1, &\n", "(block_argument)", "binary"},
		{"foo(1, &\n)\n", "(block_argument)", "binary"},
		{"foo(&)\n", "(block_argument)", "binary"},
		{"foo 1, &blk\n", "(block_argument (identifier))", "binary"},
	} {
		tree := parser.Parse([]byte(tc.src), nil)
		root := tree.RootNode()
		got := root.ToSexp()
		if root.HasError() || !strings.Contains(got, tc.want) || strings.Contains(got, tc.refuse) {
			t.Errorf("%q parses to %s, want %s and no %s", tc.src, got, tc.want, tc.refuse)
		}
		tree.Close()
	}
}
