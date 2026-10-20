package rubyextractor

import (
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// A Rails repository: config/application.rb is what makes it one.
func migrationRepo(t *testing.T, migrations map[string]string) []facts.Fact {
	t.Helper()
	files := map[string]string{
		"config/application.rb": "module Shop\n  class Application < Rails::Application\n  end\nend\n",
	}
	for name, body := range migrations {
		files["db/migrate/"+name] = body
	}
	return extractRepo(t, files)
}

func migrationClass(t *testing.T, ff []facts.Fact, name string) facts.Fact {
	t.Helper()
	for _, f := range ff {
		if f.Kind == facts.KindSymbol && f.Name == name && f.PropString("symbol_kind") == facts.SymbolClass {
			return f
		}
	}
	t.Fatalf("no class fact named %s", name)
	return facts.Fact{}
}

func propInt(t *testing.T, f facts.Fact, prop string) int {
	t.Helper()
	v, ok := f.Prop(prop)
	if !ok {
		t.Fatalf("%s carries no %s: %+v", f.Name, prop, f.Props)
	}
	n, ok := v.(int)
	if !ok {
		t.Fatalf("%s.%s = %#v, want an int", f.Name, prop, v)
	}
	return n
}

func TestMigrations_AnIndexOnAnExistingTableWithoutConcurrentlyIsUnsafe(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120000_add_index_to_comments.rb": `class AddIndexToComments < ActiveRecord::Migration[7.1]
  def change
    add_index :comments, :article_id
  end
end
`,
	})
	c := migrationClass(t, ff, "AddIndexToComments")
	if got := c.PropString("migration_version"); got != "20240101120000" {
		t.Fatalf("migration_version = %q", got)
	}
	if propInt(t, c, "migration_unsafe_indexes") != 1 {
		t.Fatalf("an index on an existing table without concurrently is unsafe: %+v", c.Props)
	}
	if got := c.PropString("migration_unsafe_index_tables"); got != "comments" {
		t.Fatalf("migration_unsafe_index_tables = %q", got)
	}
	if c.PropBool("migration_disable_ddl_transaction") {
		t.Fatalf("the transaction is kept: %+v", c.Props)
	}
}

func TestMigrations_AConcurrentIndexOutsideTheTransactionIsSafe(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120001_add_index_concurrently.rb": `class AddIndexConcurrently < ActiveRecord::Migration[7.1]
  disable_ddl_transaction!

  def change
    add_index :comments, :created_at, algorithm: :concurrently
  end
end
`,
	})
	c := migrationClass(t, ff, "AddIndexConcurrently")
	if propInt(t, c, "migration_unsafe_indexes") != 0 || propInt(t, c, "migration_concurrent_in_transaction") != 0 {
		t.Fatalf("a concurrent index outside the transaction is safe: %+v", c.Props)
	}
	if !c.PropBool("migration_disable_ddl_transaction") {
		t.Fatalf("disable_ddl_transaction! is read: %+v", c.Props)
	}
}

func TestMigrations_AConcurrentIndexInsideTheTransactionIsNamed(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120002_concurrent_in_transaction.rb": `class ConcurrentInTransaction < ActiveRecord::Migration[7.1]
  def change
    add_index :comments, :author_id, algorithm: :concurrently
  end
end
`,
	})
	c := migrationClass(t, ff, "ConcurrentInTransaction")
	if propInt(t, c, "migration_concurrent_in_transaction") != 1 {
		t.Fatalf("a concurrent index in a migration that keeps its transaction fails when run: %+v", c.Props)
	}
	if propInt(t, c, "migration_unsafe_indexes") != 0 {
		t.Fatalf("it is named apart from a non-concurrent index: %+v", c.Props)
	}
}

func TestMigrations_AnIndexOnATableTheFileCreatesIsSafe(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120003_create_tags.rb": `class CreateTags < ActiveRecord::Migration[7.1]
  def change
    create_table :tags do |t|
      t.string :name, null: false
      t.timestamps
    end
    add_index :tags, :name, unique: true
    add_index :articles, :tag_id
  end
end
`,
	})
	c := migrationClass(t, ff, "CreateTags")
	if got := c.PropString("migration_creates_tables"); got != "tags" {
		t.Fatalf("migration_creates_tables = %q", got)
	}
	if propInt(t, c, "migration_unsafe_indexes") != 1 || c.PropString("migration_unsafe_index_tables") != "articles" {
		t.Fatalf("the new table's index is safe and the existing table's is not: %+v", c.Props)
	}
}

func TestMigrations_ContinuationLinesAreOneStatement(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120004_multiline_index.rb": `class MultilineIndex < ActiveRecord::Migration[7.1]
  disable_ddl_transaction!

  def change
    add_index :comments,
              [:article_id, :created_at],
              name: "index_comments_on_article_and_time",
              algorithm: :concurrently
    add_index(
      :articles,
      :published_at
    )
  end
end
`,
	})
	c := migrationClass(t, ff, "MultilineIndex")
	if propInt(t, c, "migration_unsafe_indexes") != 1 || c.PropString("migration_unsafe_index_tables") != "articles" {
		t.Fatalf("an option on a continuation line belongs to its call: %+v", c.Props)
	}
}

func TestMigrations_NotNullWithoutADefaultIsNamed(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120005_add_columns.rb": `class AddColumns < ActiveRecord::Migration[7.1]
  def change
    add_column :articles, :slug, :string, null: false
    add_column :articles, :views, :integer, null: false, default: 0
    add_column :articles, :subtitle, :string
    change_column_null :comments, :body, false
    change_column :comments, :body, :text
    remove_column :comments, :legacy_flag, :boolean
  end
end
`,
	})
	c := migrationClass(t, ff, "AddColumns")
	if propInt(t, c, "migration_not_null_without_default") != 2 {
		t.Fatalf("a NOT NULL column without a default, and NOT NULL set on an existing column: %+v", c.Props)
	}
	if got := c.PropString("migration_not_null_columns"); got != "articles.slug comments.body" {
		t.Fatalf("migration_not_null_columns = %q", got)
	}
	if got := c.PropString("migration_changed_columns"); got != "comments.body" {
		t.Fatalf("migration_changed_columns = %q", got)
	}
	if got := c.PropString("migration_removed_columns"); got != "comments.legacy_flag" {
		t.Fatalf("migration_removed_columns = %q", got)
	}
}

func TestMigrations_ARollbackIndexIsCountedApart(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120006_drop_index.rb": `class DropIndex < ActiveRecord::Migration[7.1]
  def up
    remove_index :comments, :author_id
  end

  def down
    add_index :comments, :author_id
  end
end
`,
	})
	c := migrationClass(t, ff, "DropIndex")
	if propInt(t, c, "migration_unsafe_indexes") != 0 || propInt(t, c, "migration_rollback_unsafe_indexes") != 1 {
		t.Fatalf("an index added in down is a rollback's, counted apart: %+v", c.Props)
	}
}

func TestMigrations_AReferenceIndexesUnlessToldNot(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120007_add_references.rb": `class AddReferences < ActiveRecord::Migration[7.1]
  def change
    add_reference :comments, :editor, foreign_key: true
    add_reference :comments, :reviewer, index: false
  end
end
`,
	})
	c := migrationClass(t, ff, "AddReferences")
	if propInt(t, c, "migration_unsafe_references") != 1 {
		t.Fatalf("add_reference indexes by default, and index: false does not: %+v", c.Props)
	}
	if propInt(t, c, "migration_unsafe_indexes") != 0 {
		t.Fatalf("a reference's index is counted apart from add_index: %+v", c.Props)
	}
}

func TestMigrations_ACleanMigrationCarriesZeroCounts(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120008_create_notes.rb": `class CreateNotes < ActiveRecord::Migration[7.1]
  def change
    create_table :notes do |t|
      t.text :body
    end
  end
end
`,
	})
	c := migrationClass(t, ff, "CreateNotes")
	for _, prop := range []string{"migration_unsafe_indexes", "migration_concurrent_in_transaction", "migration_rollback_unsafe_indexes", "migration_unsafe_references", "migration_not_null_without_default"} {
		if propInt(t, c, prop) != 0 {
			t.Fatalf("%s = %v on a clean migration", prop, c.Props[prop])
		}
	}
}

func TestMigrations_AClassOutsideDbMigrateIsNotRead(t *testing.T) {
	ff := extractRepo(t, map[string]string{
		"config/application.rb": "module Shop\n  class Application < Rails::Application\n  end\nend\n",
		"lib/tasks/backfill.rb": `class Backfill < ActiveRecord::Migration[7.1]
  def change
    add_index :comments, :article_id
  end
end
`,
	})
	c := migrationClass(t, ff, "Backfill")
	if _, ok := c.Prop("migration_unsafe_indexes"); ok {
		t.Fatalf("only db/migrate holds migrations: %+v", c.Props)
	}
}

func TestMigrations_AnAppNestedInTheRepositoryIsRead(t *testing.T) {
	ff := extractRepo(t, map[string]string{
		"services/web/config/application.rb": "module Web\n  class Application < Rails::Application\n  end\nend\n",
		"services/web/db/migrate/20240101120009_add_index_to_orders.rb": `class AddIndexToOrders < ActiveRecord::Migration[7.1]
  def change
    add_index :orders, :customer_id
  end
end
`,
		"services/tools/db/migrate/20240101120010_not_an_app.rb": `class NotAnApp < ActiveRecord::Migration[7.1]
  def change
    add_index :orders, :placed_at
  end
end
`,
	})
	c := migrationClass(t, ff, "AddIndexToOrders")
	if propInt(t, c, "migration_unsafe_indexes") != 1 {
		t.Fatalf("a Rails app below the repository root runs its migrations too: %+v", c.Props)
	}
	if _, ok := migrationClass(t, ff, "NotAnApp").Prop("migration_unsafe_indexes"); ok {
		t.Fatalf("a db/migrate whose directory is no Rails app is not read")
	}
}

func TestMigrations_AnEngineMigrationKeepsItsVersion(t *testing.T) {
	ff := migrationRepo(t, map[string]string{
		"20240101120011_create_active_storage_tables.active_storage.rb": `class CreateActiveStorageTables < ActiveRecord::Migration[7.0]
  def change
    create_table :active_storage_blobs do |t|
      t.string :key, null: false
    end
    add_index :active_storage_blobs, :key, unique: true
  end
end
`,
	})
	c := migrationClass(t, ff, "CreateActiveStorageTables")
	if got := c.PropString("migration_version"); got != "20240101120011" {
		t.Fatalf("an engine's copied migration is <version>_<name>.<engine>.rb: migration_version = %q", got)
	}
}
