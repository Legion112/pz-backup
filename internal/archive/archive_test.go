package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	files := map[string]string{
		"Apocalypse/Save/map_p.bin":    "chunk data",
		"Apocalypse/Save/players.db":   "sqlite",
		"Apocalypse/Save/nested/a.txt": "deep",
	}
	writeTree(t, src, files)

	var buf bytes.Buffer
	stats, skipped, err := Create(&buf, src, "Apocalypse/Save")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != len(files) {
		t.Errorf("archived %d files, want %d", stats.Files, len(files))
	}
	if len(skipped) != 0 {
		t.Errorf("skipped %v, want none", skipped)
	}

	vstats, err := Verify(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if vstats.Files != stats.Files || vstats.Bytes != stats.Bytes {
		t.Errorf("verify saw %+v, create wrote %+v", vstats, stats)
	}

	dest := t.TempDir()
	if _, err := Extract(bytes.NewReader(buf.Bytes()), dest); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

// Extracting over an existing file must fail rather than replace it: a restore
// that silently eats the save you are trying to compare against is the exact
// accident this tool exists to prevent.
func TestExtractNeverOverwrites(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"Mode/Save/a.txt": "from archive"})

	var buf bytes.Buffer
	if _, _, err := Create(&buf, src, "Mode/Save"); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	writeTree(t, dest, map[string]string{"Mode/Save/a.txt": "already here"})

	if _, err := Extract(bytes.NewReader(buf.Bytes()), dest); err == nil {
		t.Fatal("expected an error extracting onto an existing file")
	}
	got, err := os.ReadFile(filepath.Join(dest, "Mode", "Save", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "already here" {
		t.Errorf("existing file was modified: %q", got)
	}
}

func TestVerifyRejectsTruncated(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"Mode/Save/big.bin": strings.Repeat("x", 64*1024)})

	var buf bytes.Buffer
	if _, _, err := Create(&buf, src, "Mode/Save"); err != nil {
		t.Fatal(err)
	}
	cut := buf.Bytes()[:buf.Len()/2]
	if _, err := Verify(bytes.NewReader(cut)); err == nil {
		t.Fatal("expected an error verifying a truncated archive")
	}
}

func TestCreateSkipsNonRegular(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"Mode/Save/real.txt": "hi"})
	link := filepath.Join(src, "Mode", "Save", "link.txt")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var buf bytes.Buffer
	stats, skipped, err := Create(&buf, src, "Mode/Save")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 {
		t.Errorf("archived %d regular files, want 1", stats.Files)
	}
	if len(skipped) != 1 || !strings.HasSuffix(skipped[0], "link.txt") {
		t.Errorf("skipped = %v, want the symlink named", skipped)
	}
}
