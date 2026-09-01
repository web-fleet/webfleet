package database

import "testing"

func TestRebind(t *testing.T) {
	got := Rebind(`SELECT * FROM x WHERE a=? AND b='?' AND c=?`)
	if got != `SELECT * FROM x WHERE a=$1 AND b='?' AND c=$2` {
		t.Fatal(got)
	}
}
