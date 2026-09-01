package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedFrontendSync guards the source/generated/embedded contract: the
// application's embedded web assets (internal/server/web) must be byte-identical
// to the generated output (public), which the Nift frontend build copies in via
// `make frontend`. Editing the embedded copy directly without updating the
// generated output silently diverges and lets a rebuild erase the change.
func TestEmbeddedFrontendSync(t *testing.T) {
	generated, err := filepath.Abs("../../public")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := filepath.Abs("web")
	if err != nil {
		t.Fatal(err)
	}
	// The embedded directory is a straight copy of the generated output.
	err = filepath.WalkDir(generated, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(generated, path)
		et := filepath.Join(embedded, rel)
		gi, eerr := d.Info()
		if eerr != nil {
			return eerr
		}
		if d.IsDir() {
			di, derr := os.Stat(et)
			if derr != nil || !di.IsDir() {
				t.Errorf("generated dir %s missing in embedded web", rel)
			}
			return nil
		}
		eb, rerr := os.ReadFile(et)
		if rerr != nil {
			t.Errorf("generated file %s missing in embedded web: %v", rel, rerr)
			return nil
		}
		gb := make([]byte, gi.Size())
		fh, oerr := os.Open(path)
		if oerr != nil {
			return oerr
		}
		defer fh.Close()
		if _, oerr := fh.Read(gb); oerr != nil {
			return oerr
		}
		if string(gb) != string(eb) {
			t.Errorf("embedded web/%s differs from generated %s", rel, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
