// Package sqlite contains small query helpers used by Web Fleet's SQLite-only
// storage code. Connection ownership, pooling and transactions are handled by
// database/sql; this package deliberately contains no SQLite C bindings.
package sqlite

import (
	"database/sql"
	"fmt"
)

type Value struct {
	Text  string
	Int64 int64
	Float float64
	Null  bool
	Kind  int
}

type Row map[string]Value

func Exec(db execer, query string, args ...any) error {
	_, err := db.Exec(query, args...)
	return err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func Query(db queryer, query string, args ...any) ([]Row, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0)
	for rows.Next() {
		raw := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := Row{}
		for i, name := range columns {
			v := Value{}
			switch x := raw[i].(type) {
			case nil:
				v.Null = true
			case int64:
				v.Int64 = x
				v.Kind = 1
			case float64:
				v.Float = x
				v.Kind = 2
			case bool:
				if x {
					v.Int64 = 1
				}
				v.Kind = 1
			case []byte:
				v.Text = string(x)
				v.Kind = 3
			case string:
				v.Text = x
				v.Kind = 3
			default:
				return nil, fmt.Errorf("unsupported sqlite result type %T for column %s", raw[i], name)
			}
			row[name] = v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}
