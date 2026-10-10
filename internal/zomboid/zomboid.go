// Package zomboid locates Project Zomboid's user directory and the saves in it.
//
// The game keeps everything under one directory -- ~/Zomboid on Linux and macOS,
// %USERPROFILE%\Zomboid on Windows -- with saves filed by game mode:
//
//	Zomboid/Saves/<Mode>/<SaveName>/
//
// A save is a directory, not a file. It holds chunk data, two SQLite databases
// (players.db, vehicles.db) and their journals, so copying one is a directory
// walk rather than a single read, and the databases are the part that can be
// caught mid-write while the game runs.
package zomboid

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is an opened Project Zomboid user directory.
type Dir struct {
	// Root is the Zomboid directory itself, e.g. /home/you/Zomboid.
	Root string
}

// Save identifies one save directory.
type Save struct {
	// Mode is the game mode folder: Apocalypse, Sandbox, Builder, Multiplayer.
	Mode string
	// Name is the save folder inside it.
	Name string
	// Path is the absolute path to the save directory.
	Path string
}

// ID is the stable "Mode/Name" identifier used on the command line and in
// archives. Save names are not unique across modes, so the mode is part of it.
func (s Save) ID() string { return s.Mode + "/" + s.Name }

// DefaultRoot is where the game keeps its user directory, honouring
// PZ_ZOMBOID_DIR for installs that moved it.
func DefaultRoot() string {
	if v := os.Getenv("PZ_ZOMBOID_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "Zomboid"
	}
	return filepath.Join(home, "Zomboid")
}

// Open checks that root looks like a Zomboid directory. It requires only that
// Saves/ exists: everything else in there is optional and version dependent.
func Open(root string) (*Dir, error) {
	if root == "" {
		root = DefaultRoot()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("zomboid: %w", err)
	}
	saves := filepath.Join(abs, "Saves")
	info, err := os.Stat(saves)
	if err != nil {
		return nil, fmt.Errorf("zomboid: no Saves directory under %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("zomboid: %s is not a directory", saves)
	}
	return &Dir{Root: abs}, nil
}

// SavesDir is the directory every save lives under. Archives store paths
// relative to it, so "Apocalypse/MySave/..." restores to the right mode.
func (d *Dir) SavesDir() string { return filepath.Join(d.Root, "Saves") }

// Saves lists every save, sorted by mode then name.
func (d *Dir) Saves() ([]Save, error) {
	modes, err := os.ReadDir(d.SavesDir())
	if err != nil {
		return nil, fmt.Errorf("zomboid: %w", err)
	}
	var out []Save
	for _, mode := range modes {
		if !mode.IsDir() {
			continue
		}
		names, err := os.ReadDir(filepath.Join(d.SavesDir(), mode.Name()))
		if err != nil {
			return nil, fmt.Errorf("zomboid: %w", err)
		}
		for _, name := range names {
			if !name.IsDir() {
				continue
			}
			out = append(out, Save{
				Mode: mode.Name(),
				Name: name.Name(),
				Path: filepath.Join(d.SavesDir(), mode.Name(), name.Name()),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mode != out[j].Mode {
			return out[i].Mode < out[j].Mode
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Find resolves an "Mode/Name" ID, or a bare save name when it is unambiguous
// across modes.
func (d *Dir) Find(id string) (Save, error) {
	saves, err := d.Saves()
	if err != nil {
		return Save{}, err
	}
	var matches []Save
	for _, s := range saves {
		if s.ID() == id || s.Name == id {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return Save{}, fmt.Errorf("zomboid: no save %q", id)
	case 1:
		return matches[0], nil
	default:
		var ids []string
		for _, m := range matches {
			ids = append(ids, m.ID())
		}
		return Save{}, fmt.Errorf("zomboid: %q is ambiguous: %s", id, strings.Join(ids, ", "))
	}
}

// Latest is the save the game last had open, per latestSave.ini.
//
// That file is two lines, save name then mode, which is the reverse of the path
// order -- so this checks both arrangements against the filesystem rather than
// trusting the order, and reports a clear error when neither resolves.
func (d *Dir) Latest() (Save, error) {
	path := filepath.Join(d.Root, "latestSave.ini")
	f, err := os.Open(path)
	if err != nil {
		return Save{}, fmt.Errorf("zomboid: %w", err)
	}
	defer f.Close()

	var lines []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if line := strings.TrimSpace(scan.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if err := scan.Err(); err != nil {
		return Save{}, fmt.Errorf("zomboid: reading %s: %w", path, err)
	}
	if len(lines) < 2 {
		return Save{}, fmt.Errorf("zomboid: %s has %d usable lines, want 2", path, len(lines))
	}

	for _, try := range [][2]string{{lines[1], lines[0]}, {lines[0], lines[1]}} {
		mode, name := try[0], try[1]
		p := filepath.Join(d.SavesDir(), mode, name)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return Save{Mode: mode, Name: name, Path: p}, nil
		}
	}
	return Save{}, fmt.Errorf("zomboid: %s names %q / %q but neither is a save directory", path, lines[0], lines[1])
}

// GameVersion is the build recorded in version.txt, for the manifest. A save
// can only be loaded by a build that understands it, so it is worth recording;
// it is best effort and returns "" when the file is missing.
//
// Only the first line is kept: the file carries build details on later lines
// that would turn a one-line manifest field into several.
func (d *Dir) GameVersion() string {
	b, err := os.ReadFile(filepath.Join(d.Root, "version.txt"))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}
