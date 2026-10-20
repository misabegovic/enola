package rubyextractor

import (
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

// A queue is read only where the source states it: a literal in the class's own
// body. ActiveJob and Sidekiq each let configuration change the default and each
// merge a superclass's options their own way, so a queue taken from anywhere else
// would be a derivation that did not happen, stated as a fact.
const (
	queueNotLiteral       = "queue_not_literal"
	queueOutsideClassBody = "queue_outside_class_body"
	jobWithoutQueue       = "job_without_queue"
)

type queueCounts struct {
	declared int
	misses   map[string]int
}

func (c *queueCounts) miss(reason string) {
	if c.misses == nil {
		c.misses = map[string]int{}
	}
	c.misses[reason]++
}

func (c *queueCounts) merge(o queueCounts) {
	c.declared += o.declared
	for reason, n := range o.misses {
		if c.misses == nil {
			c.misses = map[string]int{}
		}
		c.misses[reason] += n
	}
}

func (c queueCounts) total() int {
	n := 0
	for _, v := range c.misses {
		n += v
	}
	return n
}

func (w *rubyWalker) readQueueDeclaration(node *sitter.Node) {
	if recv := node.ChildByFieldName("receiver"); recv != nil && kindOf(recv) != "self" {
		return
	}
	method := rubyText(node.ChildByFieldName("method"), w.src)
	args := node.ChildByFieldName("arguments")
	var messaging string
	var value *sitter.Node
	switch method {
	case "include":
		if s := w.cur(); s != nil && isSidekiqModule(firstConstArg(args, w.src)) {
			s.sidekiqJob = true
		}
		return
	case "queue_as":
		messaging = "activejob"
		if node.ChildByFieldName("block") == nil {
			value = firstPositionalArg(args)
		}
	case "sidekiq_options":
		messaging = "sidekiq"
		var found bool
		value, found = queueOption(args, w.src)
		if !found {
			return
		}
	default:
		return
	}

	scope := w.cur()
	if scope != nil && scope.kind == "class" {
		scope.queueDeclared = true
	}
	if scope == nil || scope.kind != "class" || scope.symFactIdx < 0 || !directlyInClassBody(node) {
		w.queues.miss(queueOutsideClassBody)
		return
	}
	queue, ok := literalQueueName(value, w.src)
	if !ok {
		w.queues.miss(queueNotLiteral)
		return
	}
	w.queues.declared++
	w.out = append(w.out, facts.Fact{
		Kind:      facts.KindStorage,
		Name:      queue,
		File:      w.relFile,
		Line:      line(node),
		EndLine:   endLine(node),
		Column:    column(node),
		EndColumn: endColumn(node),
		Props: map[string]any{
			"storage_kind":               facts.StorageKindTopic,
			facts.PropMessaging:          messaging,
			facts.PropMessagingRole:      facts.MessagingRoleConsumer,
			facts.PropMessagingOperation: facts.MessagingOperationSubscribe,
			facts.PropSource:             facts.MessagingSourceRubyJobQueue,
			"code_symbol":                w.out[scope.symFactIdx].Name,
			"language":                   "ruby",
		},
		Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: w.dir}},
	})
}

// Called when a class scope closes. A class counts as a job by what Rails
// dispatches on (its superclass, a Sidekiq include) or, in a Rails repository, by
// the directory convention rails_component already reads.
func (w *rubyWalker) closeJobClass(scope *rubyScope, superclass string, symbolProps map[string]any) {
	if scope.queueDeclared {
		return
	}
	isJob := scope.sidekiqJob || railsComponent("", superclass, nil, "") == "job"
	if c, _ := symbolProps["rails_component"].(string); c == "job" {
		isJob = true
	}
	if isJob {
		w.queues.miss(jobWithoutQueue)
	}
}

func isSidekiqModule(name string) bool {
	switch normalizeConstant(name) {
	case "Sidekiq::Worker", "Sidekiq::Job":
		return true
	}
	return false
}

func directlyInClassBody(node *sitter.Node) bool {
	body := node.Parent()
	if body == nil || kindOf(body) != "body_statement" {
		return false
	}
	owner := body.Parent()
	return owner != nil && kindOf(owner) == "class"
}

func firstPositionalArg(args *sitter.Node) *sitter.Node {
	if args == nil {
		return nil
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		switch kindOf(c) {
		case "comment":
			continue
		case "pair", "hash_splat_argument", "block_argument", "splat_argument":
			return nil
		}
		return c
	}
	return nil
}

// The queue key is looked for among the call's own pairs and inside one hash
// literal argument, the two ways sidekiq_options is written; any other key leaves
// the call as no queue declaration at all.
func queueOption(args *sitter.Node, src []byte) (*sitter.Node, bool) {
	if args == nil {
		return nil, false
	}
	var pairs []*sitter.Node
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		switch kindOf(c) {
		case "pair":
			pairs = append(pairs, c)
		case "hash":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				if p := c.NamedChild(j); kindOf(p) == "pair" {
					pairs = append(pairs, p)
				}
			}
		}
	}
	for _, p := range pairs {
		key := p.ChildByFieldName("key")
		if key == nil {
			continue
		}
		var name string
		switch kindOf(key) {
		case "hash_key_symbol":
			name = rubyText(key, src)
		case "simple_symbol":
			name = strings.TrimPrefix(rubyText(key, src), ":")
		case "string":
			name, _ = plainLiteral(key, src)
		}
		if name == "queue" {
			return p.ChildByFieldName("value"), true
		}
	}
	return nil, false
}

func literalQueueName(value *sitter.Node, src []byte) (string, bool) {
	if value == nil {
		return "", false
	}
	switch kindOf(value) {
	case "simple_symbol":
		name := strings.TrimPrefix(rubyText(value, src), ":")
		return name, name != ""
	case "string", "delimited_symbol":
		return plainLiteral(value, src)
	}
	return "", false
}

// A string or quoted symbol is a literal only when its content is one run of text:
// an interpolation is a value the source does not state, and an escape is left
// alone rather than decoded by hand.
func plainLiteral(node *sitter.Node, src []byte) (string, bool) {
	var text string
	parts := 0
	for i := uint(0); i < node.NamedChildCount(); i++ {
		c := node.NamedChild(i)
		if kindOf(c) != "string_content" {
			return "", false
		}
		text = rubyText(c, src)
		parts++
	}
	return text, parts == 1 && text != ""
}
