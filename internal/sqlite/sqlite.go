package sqlite

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

type DB struct {
	mu sync.Mutex
	db *C.sqlite3
}
type Value struct {
	Text  string
	Int64 int64
	Float float64
	Null  bool
	Kind  int
}
type Row map[string]Value

func Open(path string) (*DB, error) {
	c := C.CString(path)
	defer C.free(unsafe.Pointer(c))
	var db *C.sqlite3
	if rc := C.sqlite3_open_v2(c, &db, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_CREATE|C.SQLITE_OPEN_FULLMUTEX, nil); rc != C.SQLITE_OK {
		var msg string
		if db != nil {
			msg = C.GoString(C.sqlite3_errmsg(db))
			C.sqlite3_close(db)
		}
		return nil, fmt.Errorf("sqlite open: %s", msg)
	}
	d := &DB{db: db}
	if err := d.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db == nil {
		return nil
	}
	if rc := C.sqlite3_close(d.db); rc != C.SQLITE_OK {
		return errors.New("sqlite close failed")
	}
	d.db = nil
	return nil
}
func (d *DB) Err() error {
	if d.db == nil {
		return errors.New("sqlite closed")
	}
	return errors.New(C.GoString(C.sqlite3_errmsg(d.db)))
}
func (d *DB) Exec(sql string, args ...any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := d.prepare(sql)
	if err != nil {
		return err
	}
	defer C.sqlite3_finalize(st)
	if err = d.bind(st, args); err != nil {
		return err
	}
	for {
		rc := C.sqlite3_step(st)
		if rc == C.SQLITE_DONE {
			return nil
		}
		if rc == C.SQLITE_ROW {
			continue
		}
		return d.Err()
	}
}
func (d *DB) Query(sql string, args ...any) ([]Row, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := d.prepare(sql)
	if err != nil {
		return nil, err
	}
	defer C.sqlite3_finalize(st)
	if err = d.bind(st, args); err != nil {
		return nil, err
	}
	n := int(C.sqlite3_column_count(st))
	names := make([]string, n)
	for i := range n {
		names[i] = C.GoString(C.sqlite3_column_name(st, C.int(i)))
	}
	var out []Row
	for {
		rc := C.sqlite3_step(st)
		if rc == C.SQLITE_DONE {
			return out, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, d.Err()
		}
		r := Row{}
		for i, name := range names {
			typ := C.sqlite3_column_type(st, C.int(i))
			v := Value{Kind: int(typ)}
			switch typ {
			case C.SQLITE_INTEGER:
				v.Int64 = int64(C.sqlite3_column_int64(st, C.int(i)))
			case C.SQLITE_FLOAT:
				v.Float = float64(C.sqlite3_column_double(st, C.int(i)))
			case C.SQLITE_TEXT:
				v.Text = C.GoString((*C.char)(unsafe.Pointer(C.sqlite3_column_text(st, C.int(i)))))
			case C.SQLITE_NULL:
				v.Null = true
			default:
				v.Text = C.GoString((*C.char)(unsafe.Pointer(C.sqlite3_column_text(st, C.int(i)))))
			}
			r[name] = v
		}
		out = append(out, r)
	}
}
func (d *DB) LastInsertID() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return int64(C.sqlite3_last_insert_rowid(d.db))
}
func (d *DB) prepare(sql string) (*C.sqlite3_stmt, error) {
	c := C.CString(sql)
	defer C.free(unsafe.Pointer(c))
	var st *C.sqlite3_stmt
	if rc := C.sqlite3_prepare_v2(d.db, c, -1, &st, nil); rc != C.SQLITE_OK {
		return nil, d.Err()
	}
	return st, nil
}
func (d *DB) bind(st *C.sqlite3_stmt, args []any) error {
	for i, a := range args {
		idx := C.int(i + 1)
		var rc C.int
		switch v := a.(type) {
		case nil:
			rc = C.sqlite3_bind_null(st, idx)
		case string:
			c := C.CString(v)
			rc = C.sqlite3_bind_text(st, idx, c, -1, nil)
			C.free(unsafe.Pointer(c))
		case []byte:
			if len(v) == 0 {
				rc = C.sqlite3_bind_blob(st, idx, nil, 0, nil)
			} else {
				rc = C.sqlite3_bind_blob(st, idx, unsafe.Pointer(&v[0]), C.int(len(v)), nil)
			}
		case int:
			rc = C.sqlite3_bind_int64(st, idx, C.sqlite3_int64(v))
		case int64:
			rc = C.sqlite3_bind_int64(st, idx, C.sqlite3_int64(v))
		case bool:
			if v {
				rc = C.sqlite3_bind_int(st, idx, 1)
			} else {
				rc = C.sqlite3_bind_int(st, idx, 0)
			}
		case float64:
			rc = C.sqlite3_bind_double(st, idx, C.double(v))
		default:
			return fmt.Errorf("unsupported sqlite arg %T", a)
		}
		if rc != C.SQLITE_OK {
			return d.Err()
		}
	}
	return nil
}
