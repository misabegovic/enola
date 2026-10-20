package facts

// FactKinds is every fact kind a snapshot carries, in the order
// docs/schema/facts.md tables them. A caller that needs the closed set reads
// it here, and the kinds test holds it and the documented table together.
var FactKinds = []string{
	KindModule, KindSymbol, KindRoute, KindStorage, KindDependency, KindService,
	KindIntent, KindExtraction, KindAssociation, KindTestRef, KindFileRef,
	KindLint, KindTestCase, KindRuntimeUnit,
}

// RelationKinds is every relation kind a fact carries, in the documented
// order, held to docs/schema/facts.md the same way.
var RelationKinds = []string{
	RelDeclares, RelImports, RelCalls, RelImplements, RelDependsOn,
	RelInstantiates, RelInjects, RelHasMethod, RelHandledBy, RelImplementedBy,
	RelNames, RelReads, RelWrites, RelRuns,
}
