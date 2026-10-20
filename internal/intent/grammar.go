package intent

// TokenPattern is the token grammar as a regular expression: the names of
// components, rules, services and recipes, a page's scope and a relation's
// words. It states the set validToken admits, for a caller writing a name
// before handing it over, and the grammar test holds the two together.
const TokenPattern = `^[a-z0-9-]+$`

// Dialect is one of the small pattern languages a declaration speaks, stated
// form by form, so a caller writing a pattern knows which it may write
// without learning it from a refusal.
type Dialect struct {
	Name  string        `json:"name"`
	Forms []DialectForm `json:"forms"`
	// Metacharacters are refused anywhere a form does not place them.
	Metacharacters string `json:"metacharacters"`
}

// DialectForm is one form of a dialect with an example it admits.
type DialectForm struct {
	Form    string `json:"form"`
	Example string `json:"example"`
	Means   string `json:"means"`
}

// MatchDialect is what a component's match and public lists speak, the forms
// validConstraintMatch admits and the explainer's path matcher applies.
var MatchDialect = Dialect{
	Name: "bounded-path",
	Forms: []DialectForm{
		{Form: "exact", Example: "app/models/user.rb", Means: "one repo-relative path"},
		{Form: "subtree", Example: "app/models/**", Means: "the prefix and every path under it"},
		{Form: "basename", Example: "**/*_controller.rb", Means: "a file whose own name matches, at any depth; one * around a non-empty literal"},
	},
	Metacharacters: "*?[]{}",
}

// NamePatternDialect is what name_pattern, require_name's and forbid_name's
// pattern, to_name and when_edge_to speak: ValidNamePattern admits it and
// MatchBoundedName applies it.
var NamePatternDialect = Dialect{
	Name: "bounded-name",
	Forms: []DialectForm{
		{Form: "exact", Example: "User", Means: "the name itself"},
		{Form: "prefix", Example: "Admin*", Means: "a name beginning with the literal"},
		{Form: "suffix", Example: "*Controller", Means: "a name ending with the literal"},
	},
	Metacharacters: "*?[]{}",
}
