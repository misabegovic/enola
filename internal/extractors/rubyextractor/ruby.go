package rubyextractor

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/parallel"
)

// RubyExtractor extracts architectural facts from Ruby source code using the
// tree-sitter Ruby grammar (in line with the other language extractors).
type RubyExtractor struct{}

// New creates a new RubyExtractor.
func New() *RubyExtractor {
	return &RubyExtractor{}
}

func (e *RubyExtractor) Name() string {
	return "ruby"
}

// Detect returns true if the repository looks like a Ruby project. A root
// Gemfile is the fast path; failing that (many Ruby CLIs/plugins ship no
// Gemfile), it falls back to a bounded scan for loose Ruby files — .rb/.rake
// sources or extensionless executables with a Ruby shebang — mirroring the PHP
// extractor's containsPHPFile fallback so Gemfile-less repos still get indexed.
func (e *RubyExtractor) Detect(repoPath string) (bool, error) {
	if _, err := os.Stat(filepath.Join(repoPath, "Gemfile")); err == nil {
		return true, nil
	}
	return containsRubyFile(repoPath, 3), nil
}

// containsRubyFile reports whether a Ruby file exists within maxDepth directory
// levels of root (0 = root only). Vendored and VCS directories are skipped.
// A file counts as Ruby if isRubyFile matches its name (.rb/.rake/Rakefile) or,
// for an extensionless file, it carries a Ruby shebang.
func containsRubyFile(root string, maxDepth int) bool {
	var found bool
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if found || depth > maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, ent := range entries {
			if found {
				return
			}
			name := ent.Name()
			if ent.IsDir() {
				if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
					continue
				}
				walk(filepath.Join(dir, name), depth+1)
				continue
			}
			if isRubyFile(name) || (filepath.Ext(name) == "" && hasRubyShebang(filepath.Join(dir, name))) {
				found = true
				return
			}
		}
	}
	walk(root, 0)
	return found
}

// hasRubyShebang reports whether the file at absPath begins with a Ruby shebang
// (e.g. "#!/usr/bin/env ruby"). Only the first line is read, so this is cheap
// enough to run over extensionless files during discovery. Non-Ruby shebangs
// (bash, node, …) and binary files return false.
func hasRubyShebang(absPath string) bool {
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(io.LimitReader(f, 256))
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "#!") && strings.Contains(line, "ruby")
}

// isRubySourceFile reports whether relFile should be parsed as Ruby: any file
// isRubyFile matches by extension (no I/O), or an extensionless file carrying a
// Ruby shebang. repoPath is needed to resolve the shebang read.
func isRubySourceFile(repoPath, relFile string) bool {
	if isRubyFile(relFile) {
		return true
	}
	if filepath.Ext(relFile) == "" {
		return hasRubyShebang(filepath.Join(repoPath, relFile))
	}
	return false
}

// Extract parses Ruby files and emits architectural facts.
func (e *RubyExtractor) Extract(ctx context.Context, repoPath string, files []string) ([]facts.Fact, error) {
	var allFacts []facts.Fact

	isRails := detectRailsProject(repoPath)

	// Pass 1: parse packwerk packages (builds package map and privacy boundaries).
	pkgInfo := parsePackwerk(repoPath)
	allFacts = append(allFacts, pkgInfo.facts...)

	// Pass 2: parse .rb files. Route files are parsed separately by the route
	// extractor, so they are excluded here.
	var rbFiles []string
	for _, relFile := range files {
		if !isRubySourceFile(repoPath, relFile) {
			continue
		}
		if isRails && isRouteFile(relFile) {
			continue
		}
		rbFiles = append(rbFiles, relFile)
	}

	// pkgInfo is read-only here, so per-file parsing is independent. Parse in
	// parallel and merge in file order for deterministic output.
	perFileFacts := parallel.MapFiles(ctx, rbFiles, func(relFile string) []facts.Fact {
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			log.Printf("[ruby-extractor] error reading %s: %v", relFile, err)
			return nil
		}
		exported := isPublicAPI(relFile, pkgInfo)
		// extractFileAST emits symbols, imports, mixins, constants, attrs, calls,
		// and ActiveRecord storage/associations in a single AST pass;
		// extractRubyHTTPClientFacts adds outbound HTTP-client routes.
		ff := extractFileAST(src, relFile, isRails, exported)
		ff = append(ff, extractRubyHTTPClientFacts(src, relFile)...)
		ff = append(ff, extractGraphQLRubyRoutes(src, relFile)...)
		return append(ff, extractGraphQLRubyClientOps(src, relFile)...)
	})

	// Track directories that contain Ruby files for module emission.
	modules := make(map[string]bool)
	for i, ff := range perFileFacts {
		allFacts = append(allFacts, ff...)
		modules[filepath.Dir(rbFiles[i])] = true
	}

	// Emit module facts for directories not already covered by packwerk packages.
	for dir := range modules {
		if pkgInfo.isPackage(dir) {
			continue
		}
		props := map[string]any{
			"language":    "ruby",
			"module_role": facts.ModuleRoleForPath(dir),
		}
		if isRails {
			props["framework"] = "rails"
		}
		allFacts = append(allFacts, facts.Fact{
			Kind:  facts.KindModule,
			Name:  dir,
			File:  dir,
			Props: props,
		})
	}

	// Parse Rails route files.
	if isRails {
		routeFacts := extractAllRoutes(repoPath, files)
		allFacts = append(allFacts, routeFacts...)

		assocFacts, assocUnresolved := extractAssociations(repoPath, files)
		allFacts = append(allFacts, assocFacts...)
		if fact, ok := associationCoverageFact(repoPath, len(assocFacts), assocUnresolved); ok {
			allFacts = append(allFacts, fact)
		}

		allFacts = append(allFacts, extractBroadcasts(repoPath, files)...)
	}

	// A namespace's declared table_name_prefix corrects the models nested under
	// it, before the dump is folded in: the fold matches a model to a table by
	// the name the model claims, so a claim corrected afterwards would take the
	// wrong table's column census with it and leave its own table looking
	// unclaimed.
	applyTableNamePrefixes(allFacts)

	// Schema facts from the database's own dump, folded after the model pass so
	// a table a model already claims lands its census on that model's fact.
	allFacts = append(allFacts, applySchemaDump(repoPath, allFacts)...)

	resolvedCalls, unresolvedCalls := countResolvedCalls(allFacts)
	if fact, ok := callCoverageFact(repoPath, resolvedCalls, unresolvedCalls); ok {
		allFacts = append(allFacts, fact)
	}

	// Extract Ruby calls embedded in view templates (ERB/Slim/HAML) so helpers and
	// class methods invoked only from views are not mis-reported as dead. Emits
	// reference-only KindFileRef facts (no symbols); parsed in parallel.
	var tmplFiles []string
	for _, relFile := range files {
		if isTemplateFile(relFile) || isJbuilderFile(relFile) {
			tmplFiles = append(tmplFiles, relFile)
		}
	}
	controllers := newStimulusControllerIndex(files)
	tmplFacts := parallel.MapFiles(ctx, tmplFiles, func(relFile string) []facts.Fact {
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			log.Printf("[ruby-extractor] error reading template %s: %v", relFile, err)
			return nil
		}
		ff := extractTemplateRefs(src, relFile)
		ff = append(ff, extractStimulusBindings(repoPath, relFile, src, controllers)...)
		ff = append(ff, extractRenderTargets(repoPath, relFile, src)...)
		return append(ff, extractTurboFrames(relFile, src)...)
	})
	for _, ff := range tmplFacts {
		allFacts = append(allFacts, ff...)
	}

	// Resolve constant references (inheritance, mixins, associations, calls),
	// require_relative paths, and Packwerk dependencies into internal module
	// coupling edges. Without this, Ruby imports never match module Names
	// downstream and coupling collapses to zero.
	allFacts = append(allFacts, resolveImports(allFacts, isRails)...)

	return allFacts, nil
}

// ExtractTestRefs implements plugin.TestRefExtractor. It parses test/spec files
// for the SOLE purpose of capturing their outbound references into production
// code, emitting one facts.KindTestRef fact per file that carries only RelCalls
// edges — no symbols. Test methods therefore never become dead-code candidates
// (which the orphans package explicitly excludes), and no symbol/module/route
// explainer is affected, while the dead-code detector can still see that a
// production symbol is exercised by a test and not mis-report it as dead.
// prodFiles is unused: Ruby references are resolved by constant name, not against
// the set of files that exist.
func (e *RubyExtractor) ExtractTestRefs(ctx context.Context, repoPath string, testFiles, _ []string) ([]facts.Fact, error) {
	var rbFiles []string
	for _, relFile := range testFiles {
		if isRubySourceFile(repoPath, relFile) {
			rbFiles = append(rbFiles, relFile)
		}
	}
	perFile := parallel.MapFiles(ctx, rbFiles, func(relFile string) []facts.Fact {
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			log.Printf("[ruby-extractor] error reading test file %s: %v", relFile, err)
			return nil
		}
		return extractTestRefsAST(src, relFile)
	})
	var out []facts.Fact
	for _, ff := range perFile {
		out = append(out, ff...)
	}
	return out, nil
}

// --- Rails detection ---

func detectRailsProject(repoPath string) bool {
	candidates := []string{
		filepath.Join(repoPath, "config", "application.rb"),
		filepath.Join(repoPath, "bin", "rails"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// isRubyFile returns true if the file is Ruby source: a .rb/.rake file or a
// Rakefile. Rake tasks are Ruby and call into app code (e.g. from lib/tasks/),
// so indexing them lets those calls resolve (dead-code precision).
func isRubyFile(path string) bool {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".rb") || strings.HasSuffix(lower, ".rake") {
		return true
	}
	return filepath.Base(path) == "Rakefile"
}

// OwnsFile implements plugin.FileOwner for incremental caching and the file
// census. It claims everything Extract actually reads: Ruby source, the view
// templates the reference pass parses (ERB/Slim/HAML), and Jbuilder views —
// an unclaimed-but-parsed template lied twice, reading as a vocabulary gap on
// the census while its edits failed to invalidate this extractor's cache key.
// It is extension-only (no repoPath is available to sniff shebangs), so
// extensionless Ruby executables are not tracked for incremental cache
// invalidation; edits to them won't invalidate the cache key on their own.
// This is acceptable — such files are rare and the cacheVersion bump forces a
// full re-extract when the extractor's behavior changes.
func (e *RubyExtractor) OwnsFile(relFile string) bool {
	return isRubyFile(relFile) || isTemplateFile(relFile) || isJbuilderFile(relFile)
}

// isPublicAPI checks if a file is within a packwerk package's app/public/ directory.
func isPublicAPI(relFile string, pkg *packwerkInfo) bool {
	if pkg == nil || len(pkg.packages) == 0 {
		return true
	}

	ownerPkg := pkg.ownerPackage(relFile)
	if ownerPkg == "" {
		return true
	}

	pkgCfg, ok := pkg.packages[ownerPkg]
	if !ok || !pkgCfg.enforcePrivacy {
		return true
	}

	publicDir := filepath.Join(ownerPkg, "app", "public")
	return strings.HasPrefix(relFile, publicDir+"/") || strings.HasPrefix(relFile, publicDir+"\\")
}

// countResolvedCalls splits call edges into those naming a symbol this
// extraction emitted and those naming something else, so the coverage fact
// reports a ratio rather than a total.
func countResolvedCalls(all []facts.Fact) (int, map[string]int) {
	known := make(map[string]bool, len(all))
	for _, fact := range all {
		if fact.Kind == facts.KindSymbol {
			known[fact.Name] = true
		}
	}
	resolved := 0
	unresolved := map[string]int{}
	for _, fact := range all {
		if fact.Kind != facts.KindSymbol {
			continue
		}
		for _, rel := range fact.Relations {
			if rel.Kind != facts.RelCalls {
				continue
			}
			if known[rel.Target] {
				resolved++
				continue
			}
			// Named by cause rather than lumped: a bare method name is an
			// unresolved receiver, a dotted one is a call on something untyped,
			// and a constant is a class this extraction never saw.
			switch {
			case strings.Contains(rel.Target, "."):
				unresolved["untyped_receiver"]++
			case rel.Target != "" && rel.Target[0] >= 'A' && rel.Target[0] <= 'Z':
				unresolved["unknown_constant"]++
			default:
				unresolved["unqualified_method"]++
			}
		}
	}
	return resolved, unresolved
}
