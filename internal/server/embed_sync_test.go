package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestFrontendSourceGeneratedEmbeddedSync guards the full source/generated/
// embedded frontend contract. The application frontend has three copies:
//
//	content/assets/...        canonical Nift source
//	public/assets/...         Nift-generated output (assets copied verbatim)
//	internal/server/web/...   embedded into the binary (copied from public)
//
// The three verbatim application assets must be byte-identical across all
// three copies, so editing the source without a Nift rebuild (or editing the
// embedded copy directly) is caught. The generated<->embedded tree is also
// exact in both directions, so an obsolete extra embedded file is rejected.
// The HTML page is intentionally not compared source-to-generated because
// Nift templates it.
func TestFrontendSourceGeneratedEmbeddedSync(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repo, "content")
	generated := filepath.Join(repo, "public")
	embedded := filepath.Join(repo, "internal", "server", "web")

	// Stage 1: canonical source assets must match the generated output.
	verbatim := []string{
		"assets/js/script.js",
		"assets/js/database-setup-state.js",
		"assets/css/style.css",
	}
	for _, rel := range verbatim {
		src, serr := os.ReadFile(filepath.Join(source, rel))
		if serr != nil {
			t.Fatalf("read source %s: %v", rel, serr)
		}
		gen, gerr := os.ReadFile(filepath.Join(generated, rel))
		if gerr != nil {
			t.Fatalf("read generated %s: %v", rel, gerr)
		}
		if string(src) != string(gen) {
			t.Errorf("content/%s differs from public/%s: run the Nift build", rel, rel)
		}
	}

	// Stage 2: generated and embedded trees must be exact in both directions.
	generatedFiles := map[string]string{}
	if err := filepath.WalkDir(generated, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(generated, path)
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		generatedFiles[rel] = string(b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	embeddedFiles := map[string]string{}
	if err := filepath.WalkDir(embedded, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(embedded, path)
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		embeddedFiles[rel] = string(b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for rel, gen := range generatedFiles {
		emb, ok := embeddedFiles[rel]
		if !ok {
			t.Errorf("generated %s missing in embedded web: run make frontend", rel)
			continue
		}
		if gen != emb {
			t.Errorf("embedded web/%s differs from generated %s: run make frontend", rel, rel)
		}
	}
	for rel := range embeddedFiles {
		if _, ok := generatedFiles[rel]; !ok {
			t.Errorf("stale embedded web/%s has no generated counterpart: run make frontend", rel)
		}
	}
}