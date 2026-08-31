//go:build sandbox_sqlite

// Package sqlitedriver is a temporary database/sql driver boundary for the
// constrained development sandbox. Production Web Fleet is intended to use
// the same cgo-free modernc.org/sqlite driver as Trestle. Keeping the fallback
// here means no application/storage code depends on SQLite's C API.
package sqlitedriver

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
static int wf_bind_text(sqlite3_stmt *st, int idx, const char *v) { return sqlite3_bind_text(st, idx, v, -1, SQLITE_TRANSIENT); }
static int wf_bind_blob(sqlite3_stmt *st, int idx, const void *v, int n) { return sqlite3_bind_blob(st, idx, v, n, SQLITE_TRANSIENT); }
*/
import "C"

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unsafe"
)

func init() { sql.Register("sqlite", &Driver{}) }

type Driver struct{}
type conn struct{ db *C.sqlite3 }
type stmt struct {
	c    *conn
	st   *C.sqlite3_stmt
	cols []string
}
type tx struct{ c *conn }
type result struct{ id, affected int64 }
type rows struct {
	s    *stmt
	done bool
}

func (Driver) Open(dsn string) (driver.Conn, error) {
	path, pragmas, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var db *C.sqlite3
	if rc := C.sqlite3_open_v2(cpath, &db, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_CREATE|C.SQLITE_OPEN_FULLMUTEX, nil); rc != C.SQLITE_OK {
		msg := "open failed"
		if db != nil {
			msg = C.GoString(C.sqlite3_errmsg(db))
			C.sqlite3_close(db)
		}
		return nil, fmt.Errorf("sqlite open: %s", msg)
	}
	c := &conn{db: db}
	for _, pragma := range pragmas {
		if err := c.execSimple("PRAGMA " + pragma); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, nil
}

func parseDSN(dsn string) (string, []string, error) {
	if !strings.HasPrefix(dsn, "file:") {
		return dsn, nil, nil
	}
	raw := strings.TrimPrefix(dsn, "file:")
	path, query, _ := strings.Cut(raw, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", nil, fmt.Errorf("parse sqlite dsn: %w", err)
	}
	return path, values["_pragma"], nil
}

func (c *conn) Prepare(q string) (driver.Stmt, error) {
	if c.db == nil {
		return nil, errors.New("sqlite connection closed")
	}
	cq := C.CString(q)
	defer C.free(unsafe.Pointer(cq))
	var st *C.sqlite3_stmt
	var tail *C.char
	if rc := C.sqlite3_prepare_v2(c.db, cq, -1, &st, &tail); rc != C.SQLITE_OK {
		return nil, c.err()
	}
	if st == nil {
		return nil, errors.New("sqlite statement is empty")
	}
	s := &stmt{c: c, st: st}
	n := int(C.sqlite3_column_count(st))
	s.cols = make([]string, n)
	for i := range n {
		s.cols[i] = C.GoString(C.sqlite3_column_name(st, C.int(i)))
	}
	return s, nil
}
func (c *conn) Close() error {
	if c.db == nil {
		return nil
	}
	if rc := C.sqlite3_close(c.db); rc != C.SQLITE_OK {
		return c.err()
	}
	c.db = nil
	return nil
}
func (c *conn) Begin() (driver.Tx, error) {
	if err := c.execSimple("BEGIN"); err != nil {
		return nil, err
	}
	return &tx{c: c}, nil
}
func (c *conn) execSimple(q string) error {
	cq := C.CString(q)
	defer C.free(unsafe.Pointer(cq))
	var msg *C.char
	if rc := C.sqlite3_exec(c.db, cq, nil, nil, &msg); rc != C.SQLITE_OK {
		if msg != nil {
			err := errors.New(C.GoString(msg))
			C.sqlite3_free(unsafe.Pointer(msg))
			return err
		}
		return c.err()
	}
	return nil
}
func (c *conn) err() error {
	if c.db == nil {
		return errors.New("sqlite closed")
	}
	return errors.New(C.GoString(C.sqlite3_errmsg(c.db)))
}

func (t *tx) Commit() error   { return t.c.execSimple("COMMIT") }
func (t *tx) Rollback() error { return t.c.execSimple("ROLLBACK") }

func (s *stmt) Close() error {
	if s.st == nil {
		return nil
	}
	rc := C.sqlite3_finalize(s.st)
	s.st = nil
	if rc != C.SQLITE_OK {
		return s.c.err()
	}
	return nil
}
func (s *stmt) NumInput() int { return int(C.sqlite3_bind_parameter_count(s.st)) }
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	if err := s.bind(args); err != nil {
		return nil, err
	}
	rc := C.sqlite3_step(s.st)
	if rc != C.SQLITE_DONE {
		return nil, s.c.err()
	}
	return result{id: int64(C.sqlite3_last_insert_rowid(s.c.db)), affected: int64(C.sqlite3_changes(s.c.db))}, nil
}
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	if err := s.bind(args); err != nil {
		return nil, err
	}
	return &rows{s: s}, nil
}
func (s *stmt) bind(args []driver.Value) error {
	C.sqlite3_reset(s.st)
	C.sqlite3_clear_bindings(s.st)
	for i, a := range args {
		idx := C.int(i + 1)
		var rc C.int
		switch v := a.(type) {
		case nil:
			rc = C.sqlite3_bind_null(s.st, idx)
		case string:
			cv := C.CString(v)
			rc = C.wf_bind_text(s.st, idx, cv)
			C.free(unsafe.Pointer(cv))
		case []byte:
			if len(v) == 0 {
				rc = C.wf_bind_blob(s.st, idx, nil, 0)
			} else {
				rc = C.wf_bind_blob(s.st, idx, unsafe.Pointer(&v[0]), C.int(len(v)))
			}
		case int64:
			rc = C.sqlite3_bind_int64(s.st, idx, C.sqlite3_int64(v))
		case float64:
			rc = C.sqlite3_bind_double(s.st, idx, C.double(v))
		case bool:
			if v {
				rc = C.sqlite3_bind_int(s.st, idx, 1)
			} else {
				rc = C.sqlite3_bind_int(s.st, idx, 0)
			}
		default:
			return fmt.Errorf("unsupported sqlite argument %T", a)
		}
		if rc != C.SQLITE_OK {
			return s.c.err()
		}
	}
	return nil
}
func (r result) LastInsertId() (int64, error) { return r.id, nil }
func (r result) RowsAffected() (int64, error) { return r.affected, nil }
func (r *rows) Columns() []string             { return r.s.cols }
func (r *rows) Close() error                  { r.done = true; return nil }
func (r *rows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	rc := C.sqlite3_step(r.s.st)
	if rc == C.SQLITE_DONE {
		r.done = true
		return io.EOF
	}
	if rc != C.SQLITE_ROW {
		return r.s.c.err()
	}
	for i := range dest {
		col := C.int(i)
		switch C.sqlite3_column_type(r.s.st, col) {
		case C.SQLITE_INTEGER:
			dest[i] = int64(C.sqlite3_column_int64(r.s.st, col))
		case C.SQLITE_FLOAT:
			dest[i] = float64(C.sqlite3_column_double(r.s.st, col))
		case C.SQLITE_TEXT:
			p := C.sqlite3_column_text(r.s.st, col)
			n := C.sqlite3_column_bytes(r.s.st, col)
			dest[i] = C.GoStringN((*C.char)(unsafe.Pointer(p)), n)
		case C.SQLITE_BLOB:
			n := int(C.sqlite3_column_bytes(r.s.st, col))
			if n == 0 {
				dest[i] = []byte{}
			} else {
				p := C.sqlite3_column_blob(r.s.st, col)
				dest[i] = C.GoBytes(p, C.int(n))
			}
		case C.SQLITE_NULL:
			dest[i] = nil
		default:
			return errors.New("unsupported sqlite column type " + strconv.Itoa(int(C.sqlite3_column_type(r.s.st, col))))
		}
	}
	return nil
}
