// Package rubyenqueue turns an enqueue of a Ruby job into a producer topic fact
// at the caller, naming the queue the job's own class declares.
//
// WHY A BINDER. The Ruby extractor already writes the edge an enqueue needs:
// `Notify.perform_async(id)` is a call to `Notify.perform_async`, which resolves
// to nothing because no job defines that method. The queue lives on the job
// class, usually in another file, so only the assembled store holds both ends.
//
// WHY IT REFUSES. The constant is resolved by the Ruby extractor's constant rules
// (a qualified name first, then a bare name one directory settles), within the
// caller's repository, and a class counts only when it carries exactly one queue
// of its own. A nesting that could make Ruby read the name as another class, a
// name no directory settles, and a class with no queue or several add nothing and
// are counted, because a producer on the wrong queue is a wrong fact stated
// confidently and a missing one is honest.
package rubyenqueue

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/extractors/extcoverage"
	"github.com/enola-labs/enola/internal/factpath"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/plugin"
)

type Binder struct{}

func New() *Binder { return &Binder{} }

func (b *Binder) Name() string { return "ruby-enqueue" }

// Post-link, beside the messaging-contract binder: nothing links on a queue, so
// there is nothing for the cross-repository linker to read from these facts.
func (b *Binder) Stage() plugin.BindStage { return plugin.StagePostLink }

const recordName = "ruby:enqueues"

var enqueueMethods = map[string]bool{
	"perform_later": true, "perform_async": true, "perform_in": true, "perform_at": true,
}

const (
	constantUnknown        = "constant_unknown"
	constantAmbiguous      = "constant_ambiguous"
	classWithoutQueue      = "class_without_queue"
	classWithSeveralQueues = "class_with_several_queues"
	receiverNotAConstant   = "receiver_not_a_constant"
	// Jobs.enqueue(:name) and Resque.enqueue(Klass) name the job by an argument the
	// call edge does not carry. Reading Discourse's symbol convention would be one
	// application's rule in a general binder, so it is counted and left.
	enqueueByArgument = "enqueue_by_argument"
)

type declarer struct {
	name, dir string
}

type repoIndex struct {
	qualified map[string]string
	bare      map[string][]declarer
	queues    map[string][]facts.Fact
}

type tally struct {
	resolved int
	misses   map[string]int
}

func (b *Binder) Bind(_ context.Context, store *facts.Store) error {
	// Recomputed from scratch each run, prior derivations dropped first, the same
	// idempotence the module-edges binder keeps: an append turn or a restored
	// replay would otherwise add every producer a second time.
	store.RemoveWhere(func(f facts.Fact) bool {
		return (f.Kind == facts.KindStorage && f.PropString(facts.PropSource) == facts.MessagingSourceRubyEnqueue) ||
			(f.Kind == facts.KindExtraction && f.Name == recordName)
	})
	all := store.FactsRef()
	index := buildIndex(all)

	var out []facts.Fact
	tallies := map[string]*tally{}
	count := func(repo, reason string) {
		t := tallies[repo]
		if t == nil {
			t = &tally{misses: map[string]int{}}
			tallies[repo] = t
		}
		if reason == "" {
			t.resolved++
			return
		}
		t.misses[reason]++
	}

	for _, f := range all {
		if (f.Kind != facts.KindSymbol && f.Kind != facts.KindFileRef) || f.PropString("language") != "ruby" {
			continue
		}
		for _, rel := range f.Relations {
			if rel.Kind != facts.RelCalls {
				continue
			}
			dot := strings.LastIndexByte(rel.Target, '.')
			if dot < 0 {
				continue
			}
			receiver, method := strings.TrimPrefix(rel.Target[:dot], "::"), rel.Target[dot+1:]
			if method == "enqueue" && isConstant(receiver) {
				count(f.Repo, enqueueByArgument)
				continue
			}
			if !enqueueMethods[method] {
				continue
			}
			if !isConstant(receiver) {
				count(f.Repo, receiverNotAConstant)
				continue
			}
			ix := index[f.Repo]
			class, reason := ix.resolve(receiver, namespacesOf(f), callerDir(f))
			if reason != "" {
				count(f.Repo, reason)
				continue
			}
			queues := ix.queues[class]
			switch {
			case len(queues) == 0:
				count(f.Repo, classWithoutQueue)
				continue
			case len(queues) > 1:
				count(f.Repo, classWithSeveralQueues)
				continue
			}
			count(f.Repo, "")
			out = append(out, facts.Fact{
				Kind: facts.KindStorage,
				Name: queues[0].Name,
				File: f.File,
				Line: f.Line,
				Repo: f.Repo,
				Props: map[string]any{
					"storage_kind":               facts.StorageKindTopic,
					facts.PropMessaging:          queues[0].PropString(facts.PropMessaging),
					facts.PropMessagingRole:      facts.MessagingRoleProducer,
					facts.PropMessagingOperation: facts.MessagingOperationPublish,
					facts.PropSource:             facts.MessagingSourceRubyEnqueue,
					"code_symbol":                f.Name,
					"job":                        class,
					"call":                       method,
					"language":                   "ruby",
				},
				Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: callerDir(f)}},
			})
		}
	}

	repos := make([]string, 0, len(tallies))
	for repo := range tallies {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		t := tallies[repo]
		if fact, ok := extcoverage.Fact(repo, recordName, "ruby_enqueue", t.resolved, t.misses); ok {
			fact.Repo = repo
			out = append(out, fact)
		}
	}
	if len(out) > 0 {
		store.Add(out...)
	}
	return nil
}

func buildIndex(all []facts.Fact) map[string]*repoIndex {
	index := map[string]*repoIndex{}
	get := func(repo string) *repoIndex {
		ix := index[repo]
		if ix == nil {
			ix = &repoIndex{qualified: map[string]string{}, bare: map[string][]declarer{}, queues: map[string][]facts.Fact{}}
			index[repo] = ix
		}
		return ix
	}
	for _, f := range all {
		switch {
		case f.Kind == facts.KindSymbol && f.PropString("language") == "ruby":
			switch f.PropString("symbol_kind") {
			case facts.SymbolClass, facts.SymbolInterface, facts.SymbolConstant:
			default:
				continue
			}
			name := strings.TrimPrefix(f.Name, "::")
			ix := get(f.Repo)
			dir := callerDir(f)
			// The shortest declaring directory on a collision, as the extractor's
			// constant index keeps it, so a class reopened in several files is one
			// declarer rather than a tie with itself.
			if cur, seen := ix.qualified[name]; !seen || shorterDir(dir, cur) {
				ix.qualified[name] = dir
			}
		case f.Kind == facts.KindStorage && f.PropString(facts.PropSource) == facts.MessagingSourceRubyJobQueue &&
			f.PropString(facts.PropMessagingRole) == facts.MessagingRoleConsumer:
			ix := get(f.Repo)
			class := strings.TrimPrefix(f.PropString("code_symbol"), "::")
			ix.queues[class] = append(ix.queues[class], f)
		}
	}
	for _, ix := range index {
		for name, dir := range ix.qualified {
			last := lastSegment(name)
			ix.bare[last] = append(ix.bare[last], declarer{name: name, dir: dir})
		}
		for last := range ix.bare {
			sort.Slice(ix.bare[last], func(i, j int) bool { return ix.bare[last][i].name < ix.bare[last][j].name })
		}
	}
	return index
}

// resolve applies the Ruby extractor's constant rules: the name as written if a
// class, module or constant carries it, otherwise the one declarer of its last
// segment, or among several the one sharing strictly the longest directory prefix
// with the caller. One rule is added, and it only refuses: when an enclosing
// namespace of the caller also declares the name, Ruby reads it as that class if
// the caller was written in nested modules and as the other under a compact
// class name, and a fact cannot say which.
func (ix *repoIndex) resolve(ref string, namespaces []string, dir string) (string, string) {
	if ix == nil {
		return "", constantUnknown
	}
	chosen := ""
	if _, ok := ix.qualified[ref]; ok {
		chosen = ref
	} else {
		var candidates []declarer
		for _, d := range ix.bare[lastSegment(ref)] {
			if d.name == ref || strings.HasSuffix(d.name, "::"+ref) {
				candidates = append(candidates, d)
			}
		}
		switch len(candidates) {
		case 0:
			return "", constantUnknown
		case 1:
			chosen = candidates[0].name
		default:
			best, bestScore, tie := "", 0, false
			for _, c := range candidates {
				switch s := commonPrefixSegments(dir, c.dir); {
				case s > bestScore:
					best, bestScore, tie = c.name, s, false
				case s == bestScore:
					tie = true
				}
			}
			if bestScore == 0 || tie {
				return "", constantAmbiguous
			}
			chosen = best
		}
	}
	for _, ns := range namespaces {
		if lexical := ns + "::" + ref; lexical != chosen {
			if _, ok := ix.qualified[lexical]; ok {
				return "", constantAmbiguous
			}
		}
	}
	return chosen, ""
}

// namespacesOf lists the namespaces a caller sits in, innermost first:
// Admin::UsersController#update is inside Admin::UsersController and Admin.
func namespacesOf(f facts.Fact) []string {
	if f.Kind != facts.KindSymbol {
		return nil
	}
	name := strings.TrimPrefix(f.Name, "::")
	if i := strings.IndexByte(name, '#'); i >= 0 {
		name = name[:i]
	} else if f.PropString("symbol_kind") != facts.SymbolClass && f.PropString("symbol_kind") != facts.SymbolInterface {
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[:i]
		}
	}
	var out []string
	for name != "" {
		out = append(out, name)
		i := strings.LastIndex(name, "::")
		if i < 0 {
			break
		}
		name = name[:i]
	}
	return out
}

// callerDir is the directory a fact is declared in. A fact's File carries its
// repository label in append mode while its declares target does not, so the
// label is taken off before a file-scope reference falls back to its File.
func callerDir(f facts.Fact) string {
	for _, rel := range f.Relations {
		if rel.Kind == facts.RelDeclares {
			return rel.Target
		}
	}
	return factpath.Dir(strings.TrimPrefix(filepath.ToSlash(f.File), f.Repo+"/"))
}

func isConstant(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		return name[i+2:]
	}
	return name
}

func commonPrefixSegments(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}

func shorterDir(a, b string) bool {
	sa, sb := strings.Count(a, "/"), strings.Count(b, "/")
	if sa != sb {
		return sa < sb
	}
	return a < b
}
