package rubyextractor

import (
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

type accessMode int

const (
	accessRead accessMode = 1 << iota
	accessWrite
	accessReadWrite = accessRead | accessWrite
)

// accessWrites is the persistence list of the cqrs recipe's queries-never-write
// rule, word for word, so the rule that forbids a query from writing and the
// edge that says a method writes mean the same calls.
var accessWrites = []string{"create", "create!", "delete", "delete_all", "destroy", "destroy!", "insert", "insert!", "insert_all", "save", "save!", "update", "update!", "update_all", "update_attribute", "update_attributes", "update_columns", "upsert", "upsert!", "upsert_all", "touch", "increment!", "decrement!"}

// accessReads are ActiveRecord's finders and calculations, the calls that run
// a query where they are made. where, order and the other builders are left
// out: they return a relation, and the query runs wherever it is loaded.
// find_or_initialize_by is here and not in the both list because the record it
// initializes is not saved.
var accessReads = []string{"find", "find_by", "find_by!", "take", "take!", "sole", "find_sole_by", "first", "first!", "last", "last!", "exists?", "count", "average", "minimum", "maximum", "sum", "calculate", "pluck", "pick", "ids", "find_each", "find_in_batches", "find_or_initialize_by"}

// accessBoth read and then write when the read finds nothing.
var accessBoth = []string{"find_or_create_by", "find_or_create_by!", "create_or_find_by", "create_or_find_by!"}

var accessVerbs = func() map[string]accessMode {
	out := map[string]accessMode{}
	for _, v := range accessReads {
		out[v] |= accessRead
	}
	for _, v := range accessWrites {
		out[v] |= accessWrite
	}
	for _, v := range accessBoth {
		out[v] |= accessReadWrite
	}
	return out
}()

// How a verb's receiver was written. Only the first three can name a model
// exactly, and only under the conditions resolve states.
const (
	recvNone     = "none"     // no receiver, or self
	recvConstant = "constant" // a constant as written
	recvName     = "name"     // a bare method name, or self.name: an association reader, if the class declares one
	recvLocal    = "local"    // a local, a parameter, a block parameter, or an instance, class or global variable
	recvChain    = "chain"    // the result of another call
	recvOther    = "other"    // a literal or any other expression
)

// The causes an access call names no table under, counted on ruby:access.
const (
	causeNotInAMethod           = "not_in_a_method"
	causeReceiverlessOutside    = "receiverless_outside_model"
	causeOtherConstant          = "other_constant"
	causeAssociationNotDeclared = "association_not_declared_by_owner"
	causeMethodReceiver         = "method_receiver"
	causeLocalReceiver          = "local_receiver"
	causeChainedReceiver        = "chained_receiver"
	causeOtherReceiver          = "other_receiver"
	causeVerbDefinedByModel     = "verb_defined_by_model"
)

// accessCall is one call to an ActiveRecord verb as the file walk saw it. The
// walk cannot tell a model from any other class, or an association from any
// other method, so it keeps what it read for the repository pass that can.
type accessCall struct {
	owner string // the enclosing method's symbol name, "" outside a method
	file  string
	verb  string
	recv  string
	name  string // the constant as written, or the bare name read as an association
	// nesting is the constant lookup scopes, innermost first: Module.nesting.
	nesting []string
	class   string // the enclosing class's qualified name, "" when the innermost scope is not a class
	model   bool   // the enclosing class declares an ActiveRecord model
}

// noteAccess records a call to a verb of the lists, once per method, verb and
// receiver as written, which is how the call graph dedupes its edges.
func (w *rubyWalker) noteAccess(verb string, recv *sitter.Node, locals map[string]bool) {
	if _, ok := accessVerbs[verb]; !ok {
		return
	}
	c := accessCall{owner: w.accessOwner, file: w.relFile, verb: verb}
	switch kind := kindOf(recv); {
	case recv == nil || kind == "self":
		c.recv = recvNone
	case kind == "constant" || kind == "scope_resolution":
		c.recv, c.name = recvConstant, rubyText(recv, w.src)
	case kind == "identifier":
		if name := rubyText(recv, w.src); locals[name] {
			c.recv = recvLocal
		} else {
			c.recv, c.name = recvName, name
		}
	case kind == "instance_variable" || kind == "class_variable" || kind == "global_variable":
		c.recv = recvLocal
	case kind == "call":
		if name := selfReader(recv, w.src); name != "" {
			c.recv, c.name = recvName, name
		} else {
			c.recv = recvChain
		}
	default:
		c.recv = recvOther
	}
	key := c.owner + "\x00" + c.verb + "\x00" + c.recv + "\x00" + rubyText(recv, w.src)
	if w.accessSeen == nil {
		w.accessSeen = map[string]bool{}
	}
	if w.accessSeen[key] {
		return
	}
	w.accessSeen[key] = true
	c.nesting, c.class, c.model = w.accessScope()
	w.access = append(w.access, c)
}

// selfReader returns the method name of `self.name` with no arguments, the
// explicit spelling of a receiverless association read.
func selfReader(call *sitter.Node, src []byte) string {
	recv := call.ChildByFieldName("receiver")
	method := call.ChildByFieldName("method")
	if recv == nil || method == nil || kindOf(recv) != "self" || kindOf(method) != "identifier" || call.ChildByFieldName("arguments") != nil {
		return ""
	}
	return rubyText(method, src)
}

// accessScope reads the lookup scopes and the enclosing class off the scope
// stack. An eigenclass adds nothing to either: `class << self` inside a model
// is still the model.
func (w *rubyWalker) accessScope() (nesting []string, class string, model bool) {
	prefix := ""
	for _, s := range w.scopeStack {
		if s.kind == "eigenclass" || s.name == "" {
			continue
		}
		if prefix == "" {
			prefix = s.name
		} else {
			prefix += "::" + s.name
		}
		nesting = append(nesting, prefix)
	}
	for i, j := 0, len(nesting)-1; i < j; i, j = i+1, j-1 {
		nesting[i], nesting[j] = nesting[j], nesting[i]
	}
	for i := len(w.scopeStack) - 1; i >= 0; i-- {
		s := w.scopeStack[i]
		if s.kind == "eigenclass" || s.name == "" {
			continue
		}
		if s.kind == "class" {
			return nesting, nesting[0], s.isModel
		}
		break
	}
	return nesting, "", false
}

// accessIndex is what the repository pass knows that a file walk does not.
type accessIndex struct {
	models       map[string]bool              // ActiveRecord model storage facts, by name
	declared     map[string]bool              // every class, module and model name
	methods      map[string]bool              // every method and function symbol name
	associations map[string]map[string]string // model -> association -> target model
	named        map[string]bool              // every association name some model declares
}

// resolve names the model whose table a call reads or writes, or the cause it
// names none. Exact is the whole contract: the model's class written as the
// receiver, no receiver inside the model's own class, or an association that
// class itself declares. A name that happens to be unique is a coincidence,
// not a resolution.
func (ix *accessIndex) resolve(c accessCall) (string, string) {
	if c.owner == "" {
		return "", causeNotInAMethod
	}
	switch c.recv {
	case recvNone:
		if !c.model || !ix.models[c.class] {
			return "", causeReceiverlessOutside
		}
		if ix.methods[c.class+"#"+c.verb] || ix.methods[c.class+"."+c.verb] {
			return "", causeVerbDefinedByModel
		}
		return c.class, ""
	case recvConstant:
		model := resolveConstant(c.name, c.nesting, ix.declared)
		if model == "" || !ix.models[model] {
			return "", causeOtherConstant
		}
		if ix.methods[model+"."+c.verb] {
			return "", causeVerbDefinedByModel
		}
		return model, ""
	case recvName:
		if c.model {
			if target, declared := ix.associations[c.class][c.name]; declared {
				if !ix.models[target] {
					return "", causeOtherConstant
				}
				return target, ""
			}
		}
		if ix.named[c.name] {
			return "", causeAssociationNotDeclared
		}
		return "", causeMethodReceiver
	case recvLocal:
		return "", causeLocalReceiver
	case recvChain:
		return "", causeChainedReceiver
	}
	return "", causeOtherReceiver
}

// resolveConstant follows Ruby's lexical lookup over the declared constants:
// each enclosing scope innermost first, then the top level, the first declared
// name deciding. A leading :: forces the top level. A constant declared nowhere
// in the repository resolves to nothing.
func resolveConstant(written string, nesting []string, declared map[string]bool) string {
	if top, absolute := strings.CutPrefix(written, "::"); absolute {
		if declared[top] {
			return top
		}
		return ""
	}
	for _, scope := range nesting {
		if name := scope + "::" + written; declared[name] {
			return name
		}
	}
	if declared[written] {
		return written
	}
	return ""
}

// resolveAccess adds a reads or writes relation, or both, from each method to
// the storage fact of every model it touches exactly, and counts the rest by
// cause. A repository that declares no ActiveRecord model has no table to name
// and reports nothing: its every array's count is not an access census.
func resolveAccess(all []facts.Fact, calls []accessCall) (resolved int, unresolved map[string]int, ok bool) {
	ix := &accessIndex{
		models:       map[string]bool{},
		declared:     map[string]bool{},
		methods:      map[string]bool{},
		associations: map[string]map[string]string{},
		named:        map[string]bool{},
	}
	owners := map[string]int{}
	for i, f := range all {
		switch f.Kind {
		case facts.KindStorage:
			if f.PropString("storage_kind") == "model" && f.PropString("framework") == "rails" {
				ix.models[f.Name] = true
				ix.declared[f.Name] = true
			}
		case facts.KindSymbol:
			switch f.PropString("symbol_kind") {
			case facts.SymbolClass, facts.SymbolInterface: // a Ruby module is emitted as an interface
				ix.declared[f.Name] = true
			case facts.SymbolMethod, facts.SymbolFunc:
				ix.methods[f.Name] = true
				if _, seen := owners[f.File+"\x00"+f.Name]; !seen {
					owners[f.File+"\x00"+f.Name] = i
				}
			}
		case facts.KindAssociation:
			model, name, target := f.PropString("model"), f.PropString("association"), f.PropString("target")
			if model == "" || name == "" || target == "" {
				continue
			}
			if ix.associations[model] == nil {
				ix.associations[model] = map[string]string{}
			}
			ix.associations[model][name] = target
			ix.named[name] = true
		}
	}
	if len(ix.models) == 0 {
		return 0, nil, false
	}
	unresolved = map[string]int{}
	edges := map[int]map[facts.Relation]bool{}
	for _, c := range calls {
		model, cause := ix.resolve(c)
		if cause != "" {
			unresolved[cause]++
			continue
		}
		idx, found := owners[c.file+"\x00"+c.owner]
		if !found {
			unresolved[causeNotInAMethod]++
			continue
		}
		resolved++
		if edges[idx] == nil {
			edges[idx] = map[facts.Relation]bool{}
		}
		mode := accessVerbs[c.verb]
		if mode&accessRead != 0 {
			edges[idx][facts.Relation{Kind: facts.RelReads, Target: model}] = true
		}
		if mode&accessWrite != 0 {
			edges[idx][facts.Relation{Kind: facts.RelWrites, Target: model}] = true
		}
	}
	for idx, set := range edges {
		rels := make([]facts.Relation, 0, len(set))
		for rel := range set {
			rels = append(rels, rel)
		}
		sort.Slice(rels, func(i, j int) bool {
			if rels[i].Kind != rels[j].Kind {
				return rels[i].Kind < rels[j].Kind
			}
			return rels[i].Target < rels[j].Target
		})
		all[idx].Relations = append(all[idx].Relations, rels...)
	}
	return resolved, unresolved, true
}
