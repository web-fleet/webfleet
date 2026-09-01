package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type DB struct {
	*sql.DB
	Dialect string
}

func (d *DB) DialectName() string { return d.Dialect }
func Rebind(q string) string {
	var b strings.Builder
	n := 1
	inSingle := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '\'' {
			inSingle = !inSingle
			b.WriteByte(c)
			continue
		}
		if c == '?' && !inSingle {
			b.WriteString(fmt.Sprintf("$%d", n))
			n++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
func (d *DB) query(q string) string {
	if d.Dialect == "postgres" {
		return Rebind(q)
	}
	return q
}
func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.DB.Exec(d.query(query), args...)
}
func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(d.query(query), args...)
}
func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.DB.QueryRow(d.query(query), args...)
}
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, d.query(query), args...)
}
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.query(query), args...)
}
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.query(query), args...)
}
func (d *DB) BeginTx(ctx context.Context, o *sql.TxOptions) (*Tx, error) {
	tx, e := d.DB.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &Tx{Tx: tx, Dialect: d.Dialect}, nil
}

type Tx struct {
	*sql.Tx
	Dialect string
}

func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if t.Dialect == "postgres" {
		q = Rebind(q)
	}
	return t.Tx.ExecContext(ctx, q, args...)
}
