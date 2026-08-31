package password

import "testing"

func TestHashVerify(t *testing.T) {
	h, e := Hash("correct horse")
	if e != nil {
		t.Fatal(e)
	}
	if !Verify(h, "correct horse") {
		t.Fatal("valid password rejected")
	}
	if Verify(h, "wrong") {
		t.Fatal("wrong password accepted")
	}
}
