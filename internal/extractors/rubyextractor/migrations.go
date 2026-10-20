package rubyextractor

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/enola-labs/enola/internal/extractors/extcoverage"
	"github.com/enola-labs/enola/internal/facts"
)

// migrateDir holds the migrations Rails runs, under the app's own root, which
// may sit below the repository's. A class elsewhere that inherits from
// ActiveRecord::Migration is a data script or a test double, not a step the
// deploy runs, so it is not read.
const migrateDir = "db/migrate"

// reMigrationFile is the generator's file name: a fourteen-digit timestamp,
// which is the migration's version, then the snake-cased class name, and for a
// migration an engine's install task copied in, the engine's name.
var reMigrationFile = regexp.MustCompile(`^(\d{14})_([a-z0-9_]+)(?:\.[a-z0-9_]+)?\.rb$`)

// The migration calls read, each with its first literal argument (the table)
// and, where it names one, its second (the column). Like the schema.rb reader
// beside it this is not a Ruby grammar: a call whose table is not a literal
// names nothing rather than being guessed at.
var (
	reMigrationCall = regexp.MustCompile(`^(create_table|add_index|add_reference|add_belongs_to|add_column|change_column_null|change_column|remove_column)\b\(?\s*` + literalArg + `(.*)$`)
	reSecondLiteral = regexp.MustCompile(`^\s*,\s*` + literalArg)
	reConcurrently  = regexp.MustCompile(`\balgorithm:\s*(?::concurrently|"concurrently")|:algorithm\s*=>\s*:concurrently`)
	reIndexFalse    = regexp.MustCompile(`\bindex:\s*false\b|:index\s*=>\s*false\b`)
	reNullFalse     = regexp.MustCompile(`\bnull:\s*false\b|:null\s*=>\s*false\b`)
	reDefaultOpt    = regexp.MustCompile(`\bdefault:|:default\s*=>`)
	reThirdFalse    = regexp.MustCompile(`^\s*,\s*false\b`)
	reDefLine       = regexp.MustCompile(`^def\s+(?:self\.)?([a-z_][a-z0-9_]*[!?]?)`)
)

// migrationReading is what one migration file says about its own safety.
type migrationReading struct {
	creates                 []string
	unsafeIndexTables       []string
	unsafeIndexes           int
	rollbackUnsafeIndexes   int
	concurrentInTransaction int
	unsafeReferences        int
	notNullColumns          []string
	changedColumns          []string
	removedColumns          []string
	disablesTransaction     bool
}

// indexOp is one index a statement adds, held until the whole file is read:
// whether its table is new to this migration is known only at the end.
type indexOp struct {
	table      string
	concurrent bool
	rollback   bool
	reference  bool
}

// applyMigrations reads every migration under db/migrate and records on its
// class the facts a deploy-time safety rule needs: an index built on an
// existing table without concurrently locks writes for as long as it runs, a
// concurrent one inside the migration's transaction fails, and a NOT NULL
// column without a default rewrites or scans the table under lock.
func applyMigrations(repoPath string, rbFiles []string, allFacts []facts.Fact) []facts.Fact {
	classes := map[string][]int{}
	for i := range allFacts {
		f := allFacts[i]
		if f.Kind == facts.KindSymbol && f.PropString("symbol_kind") == facts.SymbolClass {
			classes[f.File] = append(classes[f.File], i)
		}
	}

	apps := map[string]bool{}
	read, misses := 0, map[string]int{}
	for _, relFile := range rbFiles {
		app, ok := migrationApp(relFile)
		if !ok {
			continue
		}
		if _, seen := apps[app]; !seen {
			apps[app] = isRailsAppRoot(filepath.Join(repoPath, filepath.FromSlash(app)))
		}
		if !apps[app] {
			continue
		}
		m := reMigrationFile.FindStringSubmatch(filepath.Base(relFile))
		if m == nil {
			misses["not_a_migration_file_name"]++
			continue
		}
		idx, ok := migrationClassIndex(allFacts, classes[relFile], m[2])
		if !ok {
			misses["no_single_migration_class"]++
			continue
		}
		src, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(relFile)))
		if err != nil {
			misses["unreadable"]++
			continue
		}
		reading := readMigration(string(src))
		props := allFacts[idx].Props
		if props == nil {
			props = map[string]any{}
			allFacts[idx].Props = props
		}
		props["migration_version"] = m[1]
		props["migration_creates_tables"] = strings.Join(reading.creates, " ")
		props["migration_unsafe_indexes"] = reading.unsafeIndexes
		props["migration_unsafe_index_tables"] = strings.Join(reading.unsafeIndexTables, " ")
		props["migration_rollback_unsafe_indexes"] = reading.rollbackUnsafeIndexes
		props["migration_concurrent_in_transaction"] = reading.concurrentInTransaction
		props["migration_unsafe_references"] = reading.unsafeReferences
		props["migration_not_null_without_default"] = len(reading.notNullColumns)
		props["migration_not_null_columns"] = strings.Join(reading.notNullColumns, " ")
		props["migration_changed_columns"] = strings.Join(reading.changedColumns, " ")
		props["migration_removed_columns"] = strings.Join(reading.removedColumns, " ")
		props["migration_disable_ddl_transaction"] = reading.disablesTransaction
		read++
	}
	if read == 0 && len(misses) == 0 {
		return nil
	}
	if fact, ok := extcoverage.Fact(repoPath, "ruby:migrations", "ruby_migration", read, misses); ok {
		return []facts.Fact{fact}
	}
	return nil
}

// migrationApp is the app root a migration file sits under: the directory
// holding db/migrate, the repository root when it is "".
func migrationApp(relFile string) (string, bool) {
	dir := path.Dir(relFile)
	if dir != migrateDir && !strings.HasSuffix(dir, "/"+migrateDir) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimSuffix(dir, migrateDir), "/"), true
}

// isRailsAppRoot reads the same markers detectRailsProject does, for one app
// root rather than the repository's.
func isRailsAppRoot(root string) bool {
	for _, marker := range []string{"config/application.rb", "bin/rails", "config/environment.rb"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(marker))); err == nil {
			return true
		}
	}
	return false
}

// migrationClassIndex picks the class a migration file declares: the only one,
// or of several the one the file name names. A file declaring several classes
// none of which it names is not guessed at.
func migrationClassIndex(allFacts []facts.Fact, candidates []int, snakeName string) (int, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	want := camelizeClass(snakeName)
	for _, i := range candidates {
		name := allFacts[i].Name
		if name == want || strings.HasSuffix(name, "::"+want) {
			return i, true
		}
	}
	return 0, false
}

// readMigration walks the file statement by statement, a call continued over
// lines read as one, and tracks which method each statement sits in so an
// index a rollback adds is counted apart from one the migration runs.
func readMigration(src string) migrationReading {
	var (
		r       migrationReading
		creates = map[string]bool{}
		ops     []indexOp
		notNull = map[string]bool{}
		changed = map[string]bool{}
		removed = map[string]bool{}
		methods []methodScope
	)
	for _, st := range migrationStatements(src) {
		text := st.text
		if len(methods) > 0 && text == "end" && st.indent == methods[len(methods)-1].indent {
			methods = methods[:len(methods)-1]
			continue
		}
		if m := reDefLine.FindStringSubmatch(text); m != nil {
			methods = append(methods, methodScope{name: m[1], indent: st.indent})
			continue
		}
		if text == "disable_ddl_transaction!" || strings.HasPrefix(text, "disable_ddl_transaction!(") {
			r.disablesTransaction = true
			continue
		}
		m := reMigrationCall.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		call, table, rest := m[1], literalOf(m[2], m[3]), m[4]
		rollback := len(methods) > 0 && methods[len(methods)-1].name == "down"
		column := ""
		if c := reSecondLiteral.FindStringSubmatch(rest); c != nil {
			column = literalOf(c[1], c[2])
		}
		switch call {
		case "create_table":
			creates[table] = true
		case "add_index":
			ops = append(ops, indexOp{table: table, concurrent: reConcurrently.MatchString(rest), rollback: rollback})
		case "add_reference", "add_belongs_to":
			if !reIndexFalse.MatchString(rest) {
				ops = append(ops, indexOp{table: table, concurrent: reConcurrently.MatchString(rest), rollback: rollback, reference: true})
			}
		case "add_column":
			if column != "" && reNullFalse.MatchString(rest) && !reDefaultOpt.MatchString(rest) {
				notNull[table+"."+column] = true
			}
		case "change_column_null":
			if column != "" {
				after := reSecondLiteral.ReplaceAllString(rest, "")
				if reThirdFalse.MatchString(after) {
					notNull[table+"."+column] = true
				}
			}
		case "change_column":
			if column != "" {
				changed[table+"."+column] = true
			}
		case "remove_column":
			if column != "" {
				removed[table+"."+column] = true
			}
		}
	}

	unsafeTables := map[string]bool{}
	for _, op := range ops {
		if creates[op.table] {
			continue
		}
		switch {
		case op.concurrent && !r.disablesTransaction:
			r.concurrentInTransaction++
		case op.concurrent:
		case op.rollback:
			r.rollbackUnsafeIndexes++
		case op.reference:
			r.unsafeReferences++
		default:
			r.unsafeIndexes++
			unsafeTables[op.table] = true
		}
	}
	r.creates = sortedKeys(creates)
	r.unsafeIndexTables = sortedKeys(unsafeTables)
	r.notNullColumns = sortedKeys(notNull)
	r.changedColumns = sortedKeys(changed)
	r.removedColumns = sortedKeys(removed)
	return r
}

type methodScope struct {
	name   string
	indent int
}

type migrationStatement struct {
	text   string
	indent int
}

// migrationStatements joins a call continued over lines (an open bracket, or a
// trailing comma or backslash) into one statement, so an option on a
// continuation line belongs to its call. A whole-line comment is dropped.
func migrationStatements(src string) []migrationStatement {
	var (
		out     []migrationStatement
		pending strings.Builder
		indent  int
		depth   int
	)
	for _, raw := range strings.Split(src, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if pending.Len() == 0 {
			indent = len(line) - len(strings.TrimLeft(line, " \t"))
		} else {
			pending.WriteString(" ")
		}
		pending.WriteString(trimmed)
		depth += bracketDelta(trimmed)
		if depth > 0 || strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "\\") {
			continue
		}
		out = append(out, migrationStatement{text: pending.String(), indent: indent})
		pending.Reset()
		depth = 0
	}
	if pending.Len() > 0 {
		out = append(out, migrationStatement{text: pending.String(), indent: indent})
	}
	return out
}

// bracketDelta counts the brackets a line opens minus those it closes, outside
// string literals.
func bracketDelta(line string) int {
	delta := 0
	var quote rune
	for _, c := range line {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			delta++
		case c == ')' || c == ']' || c == '}':
			delta--
		}
	}
	return delta
}
