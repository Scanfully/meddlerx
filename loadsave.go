package meddlerx

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type dbErr struct {
	msg string
	err error
}

func (err *dbErr) Error() string {
	return fmt.Sprintf("%s: %v", err.msg, err.err)
}

// DriverErr returns the original error as returned by the database driver
// if the error comes from the driver, with the second value set to true.
// Otherwise, it returns err itself with false as second value.
func DriverErr(err error) (error, bool) {
	if dbe, ok := err.(*dbErr); ok {
		return dbe.err, true
	}
	return err, false
}

// Querier is a generic interface for database query operations.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

/*
// DB is a generic database interface, matching both *sql.Db and *sql.Tx
type DB interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}
*/

// Load loads a record using a query for the primary key field.
// Returns sql.ErrNoRows if not found.
func (d *Database) Load(ctx context.Context, db Querier, table string, dst interface{}, pk int64) error {
	columns, err := d.ColumnsQuoted(dst, true)
	if err != nil {
		return err
	}

	// make sure we have a primary key field
	pkName, _, err := d.PrimaryKey(dst)
	if err != nil {
		return err
	}
	if pkName == "" {
		return fmt.Errorf("meddler.Load: no primary key field found")
	}

	// run the query
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s", columns, d.quotedTable(table), d.quoted(pkName), d.Placeholder)

	rows, err := db.QueryContext(ctx, q, pk)
	if err != nil {
		return &dbErr{msg: "meddler.Load: DB error in Query", err: err}
	}

	// scan the row
	return d.ScanRow(rows, dst)
}

// Load using the Default Database type
func Load(ctx context.Context, db Querier, table string, dst interface{}, pk int64) error {
	return Default.Load(ctx, db, table, dst, pk)
}

// Insert performs an INSERT query for the given record.
// If the record has a primary key flagged, it must be zero, and it
// will be set to the newly-allocated primary key value from the database
// as returned by LastInsertId.
func (d *Database) Insert(ctx context.Context, db Querier, table string, src interface{}) error {
	pkName, pkValue, err := d.PrimaryKey(src)
	if err != nil {
		return err
	}
	if pkName != "" && pkValue != 0 {
		return fmt.Errorf("meddler.Insert: primary key must be zero")
	}

	// gather the query parts
	namesPart, err := d.ColumnsQuoted(src, false)
	if err != nil {
		return err
	}
	valuesPart, err := d.PlaceholdersString(src, false)
	if err != nil {
		return err
	}
	values, err := d.Values(src, false)
	if err != nil {
		return err
	}

	// run the query
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", d.quotedTable(table), namesPart, valuesPart)
	if d.UseReturningToGetID && pkName != "" {
		q += " RETURNING " + d.quoted(pkName)
		var newPk int64
		err := db.QueryRowContext(ctx, q, values...).Scan(&newPk)
		if err != nil {
			return &dbErr{msg: "meddler.Insert: DB error in QueryRow", err: err}
		}
		if err = d.SetPrimaryKey(src, newPk); err != nil {
			return fmt.Errorf("meddler.Insert: Error saving updated pk: %v", err)
		}
	} else if pkName != "" {
		result, err := db.ExecContext(ctx, q, values...)
		if err != nil {
			return &dbErr{msg: "meddler.Insert: DB error in Exec", err: err}
		}

		// save the new primary key
		newPk, err := result.LastInsertId()
		if err != nil {
			return &dbErr{msg: "meddler.Insert: DB error getting new primary key value", err: err}
		}
		if err = d.SetPrimaryKey(src, newPk); err != nil {
			return fmt.Errorf("meddler.Insert: Error saving updated pk: %v", err)
		}
	} else {
		// no primary key, so no need to lookup new value
		_, err := db.ExecContext(ctx, q, values...)
		if err != nil {
			return &dbErr{msg: "meddler.Insert: DB error in Exec", err: err}
		}
	}

	return nil
}

// Insert using the Default Database type
func Insert(ctx context.Context, db Querier, table string, src interface{}) error {
	return Default.Insert(ctx, db, table, src)
}

// Update performs and UPDATE query for the given record.
// The record must have an integer primary key field that is non-zero,
// and it will be used to select the database row that gets updated.
func (d *Database) Update(ctx context.Context, db Querier, table string, src interface{}) error {
	// gather the query parts
	names, err := d.Columns(src, false)
	if err != nil {
		return err
	}
	placeholders, err := d.Placeholders(src, false)
	if err != nil {
		return err
	}
	values, err := d.Values(src, false)
	if err != nil {
		return err
	}

	// form the column=placeholder pairs
	var pairs []string
	for i := 0; i < len(names) && i < len(placeholders); i++ {
		pair := fmt.Sprintf("%s=%s", d.quoted(names[i]), placeholders[i])
		pairs = append(pairs, pair)
	}

	pkName, pkValue, err := d.PrimaryKey(src)
	if err != nil {
		return err
	}
	if pkName == "" {
		return fmt.Errorf("meddler.Update: no primary key field")
	}
	if pkValue < 1 {
		return fmt.Errorf("meddler.Update: primary key must be an integer > 0")
	}
	ph := d.placeholder(len(placeholders) + 1)

	// run the query
	q := fmt.Sprintf("UPDATE %s SET %s WHERE %s=%s", d.quotedTable(table),
		strings.Join(pairs, ","),
		d.quoted(pkName), ph)
	values = append(values, pkValue)

	if _, err := db.ExecContext(ctx, q, values...); err != nil {
		return &dbErr{msg: "meddler.Update: DB error in Exec", err: err}
	}

	return nil
}

// Update using the Default Database type
func Update(ctx context.Context, db Querier, table string, src interface{}) error {
	return Default.Update(ctx, db, table, src)
}

// Save performs an INSERT or an UPDATE, depending on whether or not
// a primary keys exists and is non-zero.
func (d *Database) Save(ctx context.Context, db Querier, table string, src interface{}) error {
	pkName, pkValue, err := d.PrimaryKey(src)
	if err != nil {
		return err
	}
	if pkName != "" && pkValue != 0 {
		return d.Update(ctx, db, table, src)
	}

	return d.Insert(ctx, db, table, src)
}

// Save using the Default Database type
func Save(ctx context.Context, db Querier, table string, src interface{}) error {
	return Default.Save(ctx, db, table, src)
}

// QueryRow performs the given query with the given arguments, scanning a
// single row of results into dst. Returns sql.ErrNoRows if there was no
// result row.
func (d *Database) QueryRow(ctx context.Context, db Querier, dst interface{}, query string, args ...interface{}) error {
	// perform the query
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}

	// gather the result
	return d.ScanRow(rows, dst)
}

// QueryRow using the Default Database type
func QueryRow(ctx context.Context, db Querier, dst interface{}, query string, args ...interface{}) error {
	return Default.QueryRow(ctx, db, dst, query, args...)
}

// QueryAll performs the given query with the given arguments, scanning
// all results rows into dst.
func (d *Database) QueryAll(ctx context.Context, db Querier, dst interface{}, query string, args ...interface{}) error {
	// perform the query
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}

	// gather the results
	return d.ScanAll(rows, dst)
}

// QueryAll using the Default Database type
func QueryAll(ctx context.Context, db Querier, dst interface{}, query string, args ...interface{}) error {
	return Default.QueryAll(ctx, db, dst, query, args...)
}

// quotedTable returns the properly quoted table name, handling optional schema (e.g., schema.table)
func (d *Database) quotedTable(table string) string {
	parts := strings.Split(table, ".")
	for i, part := range parts {
		parts[i] = d.quoted(part)
	}
	return strings.Join(parts, ".")
}
