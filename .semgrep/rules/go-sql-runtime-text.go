// Fixture for go-sql-runtime-text, checked by `semgrep --test .semgrep/rules`
// (scripts/semgrep-check.sh). Go tooling ignores this dot-directory.
package fixture

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func unsafe(ctx context.Context, db *sql.DB, tx *sql.Tx, x string) {
	// ruleid: go-sql-runtime-text
	db.Query(fmt.Sprintf("SELECT * FROM t WHERE a = '%s'", x))
	q := "DELETE FROM t WHERE a = '" + x + "'"
	// ruleid: go-sql-runtime-text
	db.Exec(q)
	var b strings.Builder
	b.WriteString("SELECT * FROM t WHERE a = ")
	b.WriteString(x)
	// ruleid: go-sql-runtime-text
	tx.QueryRowContext(ctx, b.String())
	// ruleid: go-sql-runtime-text
	db.PrepareContext(ctx, fmt.Sprintf("UPDATE %s SET a = 1", x))
}

func safe(ctx context.Context, db *sql.DB, x string) {
	// ok: go-sql-runtime-text
	db.Query("SELECT * FROM t WHERE a = ?", x)
	// ok: go-sql-runtime-text
	db.ExecContext(ctx, "DELETE FROM t WHERE a = $1", x)
	// ok: go-sql-runtime-text
	db.QueryRowContext(ctx, "SELECT a "+"FROM t WHERE b = ?", x)
}
