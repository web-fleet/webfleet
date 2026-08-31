package store

import "testing"

func TestMigrationsAreIdempotent(t *testing.T) {
	d := t.TempDir()
	s, e := Open(d)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.SchemaVersion()
	if e != nil || v != schemaVersion {
		t.Fatalf("version=%d err=%v", v, e)
	}
	s.Close()
	s, e = Open(d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	v, e = s.SchemaVersion()
	if e != nil || v != schemaVersion {
		t.Fatalf("second version=%d err=%v", v, e)
	}
}
