package zomboid

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture builds a Zomboid directory with the saves named, and whatever
// latestSave.ini content is given (empty writes no file).
func fixture(t *testing.T, latest string, saves ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, s := range saves {
		if err := os.MkdirAll(filepath.Join(root, "Saves", s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if latest != "" {
		if err := os.WriteFile(filepath.Join(root, "latestSave.ini"), []byte(latest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestOpenRequiresSaves(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("expected an error for a directory with no Saves/")
	}
}

func TestSavesSorted(t *testing.T) {
	root := fixture(t, "", "Sandbox/zeta", "Apocalypse/beta", "Apocalypse/alpha")
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Saves()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Apocalypse/alpha", "Apocalypse/beta", "Sandbox/zeta"}
	if len(got) != len(want) {
		t.Fatalf("got %d saves, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID() != want[i] {
			t.Errorf("save %d: got %q, want %q", i, got[i].ID(), want[i])
		}
	}
}

func TestFind(t *testing.T) {
	root := fixture(t, "", "Apocalypse/one", "Sandbox/one", "Builder/only")
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	if s, err := d.Find("Apocalypse/one"); err != nil || s.Mode != "Apocalypse" {
		t.Errorf("by full ID: %v %v", s, err)
	}
	if s, err := d.Find("only"); err != nil || s.ID() != "Builder/only" {
		t.Errorf("by unique bare name: %v %v", s, err)
	}
	// "one" exists in two modes, so a bare name must not silently pick one.
	if _, err := d.Find("one"); err == nil {
		t.Error("expected an ambiguity error for a name present in two modes")
	}
	if _, err := d.Find("nope"); err == nil {
		t.Error("expected an error for a save that does not exist")
	}
}

// The game writes latestSave.ini as name-then-mode, which is the reverse of the
// path order. Both arrangements must resolve, because the only thing that
// settles it is which one is a real directory.
func TestLatestBothLineOrders(t *testing.T) {
	for _, tc := range []struct {
		name string
		ini  string
	}{
		{"name then mode", "MySave\nApocalypse\n"},
		{"mode then name", "Apocalypse\nMySave\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t, tc.ini, "Apocalypse/MySave")
			d, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Latest()
			if err != nil {
				t.Fatal(err)
			}
			if got.ID() != "Apocalypse/MySave" {
				t.Errorf("got %q, want Apocalypse/MySave", got.ID())
			}
		})
	}
}

func TestLatestMissingSave(t *testing.T) {
	root := fixture(t, "Ghost\nApocalypse\n", "Apocalypse/Real")
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Latest(); err == nil {
		t.Fatal("expected an error when latestSave.ini names a save that is not there")
	}
}

func TestGameVersionBestEffort(t *testing.T) {
	root := fixture(t, "", "Apocalypse/a")
	d, _ := Open(root)
	if v := d.GameVersion(); v != "" {
		t.Errorf("missing version.txt should give \"\", got %q", v)
	}
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte("42.21.0 abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := d.GameVersion(); v != "42.21.0 abc" {
		t.Errorf("got %q", v)
	}
}

// version.txt has the build on the first line and extra detail after it; the
// manifest wants one line, not the whole file.
func TestGameVersionFirstLineOnly(t *testing.T) {
	root := fixture(t, "", "Apocalypse/a")
	d, _ := Open(root)
	body := "42.21.0 4a0e9546ec\nrevision=4a0e9546ec pzbullet=1.0.0.28\n"
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := d.GameVersion(); v != "42.21.0 4a0e9546ec" {
		t.Errorf("got %q, want just the first line", v)
	}
}
