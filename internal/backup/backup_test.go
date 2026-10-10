package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Legion112/pz-backup/internal/zomboid"
)

func fixture(t *testing.T) (*zomboid.Dir, zomboid.Save) {
	t.Helper()
	root := t.TempDir()
	save := filepath.Join(root, "Saves", "Apocalypse", "MySave")
	if err := os.MkdirAll(save, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"map_p.bin":          "chunks",
		"players.db":         "sqlite",
		"players.db-journal": "",
	} {
		if err := os.WriteFile(filepath.Join(save, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte("42.21.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := zomboid.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return d, zomboid.Save{Mode: "Apocalypse", Name: "MySave", Path: save}
}

func TestRunWritesArchiveAndManifest(t *testing.T) {
	d, save := fixture(t)
	out := t.TempDir()

	res, err := Run(d, save, Options{OutDir: out, Note: "before the escort", ToolVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.ArchivePath); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

	b, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Save != "Apocalypse/MySave" {
		t.Errorf("save = %q", m.Save)
	}
	if m.Files != 3 {
		t.Errorf("files = %d, want 3", m.Files)
	}
	if m.GameVersion != "42.21.0" {
		t.Errorf("game version = %q", m.GameVersion)
	}
	if m.Note != "before the escort" {
		t.Errorf("note = %q", m.Note)
	}
	if len(m.ArchiveSHA256) != 64 {
		t.Errorf("sha256 = %q", m.ArchiveSHA256)
	}
	// Nothing is touching the fixture, so the copy must read as clean.
	if !m.Quiescent {
		t.Errorf("quiescent = false: changed=%v journals=%v", m.ChangedDuringCopy, m.OpenJournals)
	}
}

// The central promise: running twice never costs you the first backup.
func TestRunNeverOverwrites(t *testing.T) {
	d, save := fixture(t)
	out := t.TempDir()

	first, err := Run(d, save, Options{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(d, save, Options{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if first.ArchivePath == second.ArchivePath {
		t.Fatalf("both runs wrote %s", first.ArchivePath)
	}
	for _, p := range []string{first.ArchivePath, second.ArchivePath, first.ManifestPath, second.ManifestPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went missing: %v", p, err)
		}
	}
}

// Two runs inside the same second collide on the timestamped name. createNew
// has to resolve that without touching what is already there.
func TestCreateNewCollides(t *testing.T) {
	dir := t.TempDir()

	p1, f1, err := createNew(dir, "same", ".tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f1.WriteString("first"); err != nil {
		t.Fatal(err)
	}
	f1.Close()

	p2, f2, err := createNew(dir, "same", ".tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	f2.Close()

	if p1 == p2 {
		t.Fatalf("both calls returned %s", p1)
	}
	if !strings.HasSuffix(p2, "same-2.tar.zst") {
		t.Errorf("second path = %s, want the -2 suffix", filepath.Base(p2))
	}
	body, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "first" {
		t.Errorf("first file was clobbered: %q", body)
	}
}

func TestDiffNamesWhatMoved(t *testing.T) {
	now := time.Now()
	before := map[string]state{
		"steady.bin":  {size: 10, modTime: now},
		"grew.bin":    {size: 10, modTime: now},
		"touched.bin": {size: 10, modTime: now},
		"gone.bin":    {size: 10, modTime: now},
	}
	after := map[string]state{
		"steady.bin":  {size: 10, modTime: now},
		"grew.bin":    {size: 20, modTime: now},
		"touched.bin": {size: 10, modTime: now.Add(time.Second)},
		"new.bin":     {size: 1, modTime: now},
	}
	got := strings.Join(diff(before, after), " ")
	for _, want := range []string{"grew.bin", "touched.bin", "gone.bin (removed)", "new.bin (added)"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q; got %q", want, got)
		}
	}
	if strings.Contains(got, "steady.bin") {
		t.Errorf("diff reported an unchanged file: %q", got)
	}
}

// A zero-length journal is the game's resting state. Bytes in it mean a
// transaction was open and the database copy may not load.
func TestOpenJournals(t *testing.T) {
	files := map[string]state{
		"players.db":          {size: 4096},
		"players.db-journal":  {size: 0},
		"vehicles.db-journal": {size: 512},
	}
	got := openJournals(files)
	if len(got) != 1 || got[0] != "vehicles.db-journal" {
		t.Errorf("got %v, want [vehicles.db-journal]", got)
	}
}
