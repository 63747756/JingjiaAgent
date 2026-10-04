package repo

import (
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
)

func TestVMLifecycleQueriesRetainPostgresRowLock(t *testing.T) {
	for _, name := range []string{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
		selector := sql.Dialect(name).Select("id").From(sql.Table("virtual_machines"))
		lockVMRows(selector)
		query, _ := selector.Query()
		if strings.Contains(query, "FOR UPDATE") != (name != dialect.SQLite) || selector.Err() != nil {
			t.Fatalf("VM lifecycle lock changed for %s: %s", name, query)
		}
	}
}
