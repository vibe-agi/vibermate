package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// scanColumnsForForbiddenValues reports every `table.column` whose stored bytes
// contain one of the forbidden sequences.
//
// It enumerates the live schema rather than a fixed list of tables, so a column
// added later cannot silently become an unscanned hiding place for a credential
// value. Comparison is done on BLOB casts so a value is found whether it was
// stored as text or as bytes.
func scanColumnsForForbiddenValues(
	ctx context.Context,
	database *sql.DB,
	forbidden []string,
) ([]string, error) {
	tables, err := scannableTables(ctx, database)
	if err != nil {
		return nil, err
	}
	var hits []string
	for _, table := range tables {
		columns, err := scannableColumns(ctx, database, table)
		if err != nil {
			return nil, err
		}
		for _, column := range columns {
			for _, value := range forbidden {
				var count int
				query := fmt.Sprintf(
					`SELECT COUNT(*) FROM %s
					  WHERE %s IS NOT NULL
					    AND instr(CAST(%s AS BLOB), CAST(? AS BLOB)) > 0`,
					quoteIdentifier(table),
					quoteIdentifier(column),
					quoteIdentifier(column),
				)
				if err := database.QueryRowContext(
					ctx, query, value,
				).Scan(&count); err != nil {
					return nil, fmt.Errorf(
						"scan %s.%s: %w", table, column, err,
					)
				}
				if count > 0 {
					hits = append(hits, fmt.Sprintf(
						"%s.%s contains %q in %d row(s)",
						table, column, value, count,
					))
				}
			}
		}
	}
	sort.Strings(hits)
	return hits, nil
}

func scannableTables(
	ctx context.Context,
	database *sql.DB,
) ([]string, error) {
	rows, err := database.QueryContext(
		ctx,
		`SELECT name FROM sqlite_master
		  WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		  ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list stored tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// scannableColumns keeps the columns that can hold a credential value. INTEGER
// and REAL columns cannot, and excluding them keeps the scan honest about what
// it actually examined.
func scannableColumns(
	ctx context.Context,
	database *sql.DB,
	table string,
) ([]string, error) {
	rows, err := database.QueryContext(
		ctx,
		fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(table)),
	)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var (
			index        int
			name         string
			declaredType string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(
			&index, &name, &declaredType, &notNull, &defaultValue, &primaryKey,
		); err != nil {
			return nil, err
		}
		switch strings.ToUpper(strings.TrimSpace(declaredType)) {
		case "TEXT", "BLOB", "ANY", "":
			columns = append(columns, name)
		}
	}
	return columns, rows.Err()
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
