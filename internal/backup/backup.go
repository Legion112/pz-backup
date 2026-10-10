// Package backup copies one save into a new archive and says how much to trust
// the result.
//
// Two rules shape everything here.
//
// Never overwrite. A backup is only worth having if taking another one cannot
// destroy it, so names carry a timestamp, collisions get a counter, and the
// archive is created with O_EXCL -- the open fails rather than truncating an
// existing file. Nothing in this package opens an existing file for writing.
//
// Say when the copy is suspect. Project Zomboid is usually running when someone
// decides to back up, and a save copied mid-write can be subtly broken in ways
// that only surface when you try to load it. This cannot lock the game out of
// its own files, so instead it measures: every file's size and modification
// time are recorded before and after the copy, and any that moved are named in
// the manifest. An open SQLite journal is reported the same way.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Legion112/pz-backup/internal/archive"
	"github.com/Legion112/pz-backup/internal/zomboid"
)

// DefaultOutDir is where archives go, honouring PZ_BACKUP_DIR.
func DefaultOutDir() string {
	if v := os.Getenv("PZ_BACKUP_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "zomboid-backups"
	}
	return filepath.Join(home, "zomboid-backups")
}

// Manifest is the JSON sidecar written next to each archive.
//
// It is written last and only on success, so an archive with no manifest beside
// it is an interrupted run and should not be trusted.
type Manifest struct {
	Tool        string    `json:"tool"`
	ToolVersion string    `json:"tool_version"`
	Created     time.Time `json:"created"`
	Note        string    `json:"note,omitempty"`

	Save        string `json:"save"`
	Source      string `json:"source"`
	GameVersion string `json:"game_version,omitempty"`

	Archive       string `json:"archive"`
	ArchiveBytes  int64  `json:"archive_bytes"`
	ArchiveSHA256 string `json:"archive_sha256"`

	Files int   `json:"files"`
	Dirs  int   `json:"dirs"`
	Bytes int64 `json:"bytes"`

	// Quiescent is true when nothing in the save changed while it was being
	// copied and no SQLite journal was mid-transaction.
	Quiescent         bool     `json:"quiescent"`
	ChangedDuringCopy []string `json:"changed_during_copy,omitempty"`
	OpenJournals      []string `json:"open_journals,omitempty"`
	Skipped           []string `json:"skipped,omitempty"`

	Restore string `json:"restore"`
}

// Result is what Run produced.
type Result struct {
	Manifest     Manifest
	ArchivePath  string
	ManifestPath string
}

// Options configures one backup.
type Options struct {
	OutDir string
	Note   string
	// ToolVersion is recorded in the manifest.
	ToolVersion string
}

// Run copies save into a new archive under opts.OutDir.
func Run(dir *zomboid.Dir, save zomboid.Save, opts Options) (Result, error) {
	var res Result

	outDir := opts.OutDir
	if outDir == "" {
		outDir = DefaultOutDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return res, fmt.Errorf("backup: %w", err)
	}

	before, err := scan(save.Path)
	if err != nil {
		return res, err
	}

	base := fmt.Sprintf("%s_%s_%s", save.Mode, save.Name, time.Now().Format("2006-01-02_15-04-05"))
	archivePath, f, err := createNew(outDir, base, ".tar.zst")
	if err != nil {
		return res, err
	}

	// Hash while writing so verification later has something to compare against
	// without a second pass over the source.
	sum := sha256.New()
	rel, err := filepath.Rel(dir.SavesDir(), save.Path)
	if err != nil {
		f.Close()
		os.Remove(archivePath)
		return res, fmt.Errorf("backup: %w", err)
	}

	stats, skipped, err := archive.Create(io.MultiWriter(f, sum), dir.SavesDir(), rel)
	if err != nil {
		f.Close()
		os.Remove(archivePath)
		return res, err
	}
	if err := f.Close(); err != nil {
		os.Remove(archivePath)
		return res, fmt.Errorf("backup: %w", err)
	}

	after, err := scan(save.Path)
	if err != nil {
		return res, err
	}

	info, err := os.Stat(archivePath)
	if err != nil {
		return res, fmt.Errorf("backup: %w", err)
	}

	changed := diff(before, after)
	journals := openJournals(before)

	m := Manifest{
		Tool:              "pz-backup",
		ToolVersion:       opts.ToolVersion,
		Created:           time.Now(),
		Note:              opts.Note,
		Save:              save.ID(),
		Source:            save.Path,
		GameVersion:       dir.GameVersion(),
		Archive:           filepath.Base(archivePath),
		ArchiveBytes:      info.Size(),
		ArchiveSHA256:     hex.EncodeToString(sum.Sum(nil)),
		Files:             stats.Files,
		Dirs:              stats.Dirs,
		Bytes:             stats.Bytes,
		Quiescent:         len(changed) == 0 && len(journals) == 0,
		ChangedDuringCopy: changed,
		OpenJournals:      journals,
		Skipped:           skipped,
		Restore:           fmt.Sprintf("pz-backup restore %s", filepath.Base(archivePath)),
	}

	manifestPath, mf, err := createNew(outDir, strings.TrimSuffix(filepath.Base(archivePath), ".tar.zst"), ".manifest.json")
	if err != nil {
		return res, err
	}
	enc := json.NewEncoder(mf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		mf.Close()
		return res, fmt.Errorf("backup: %w", err)
	}
	if err := mf.Close(); err != nil {
		return res, fmt.Errorf("backup: %w", err)
	}

	res.Manifest = m
	res.ArchivePath = archivePath
	res.ManifestPath = manifestPath
	return res, nil
}

// createNew opens dir/base+ext for writing, failing rather than overwriting.
//
// On collision it tries base-2, base-3 and so on. A collision is already
// unlikely -- the name carries a one-second timestamp -- but two runs started in
// the same second must not be allowed to land on the same file, and O_EXCL makes
// that a kernel-level guarantee rather than a check-then-write race.
func createNew(dir, base, ext string) (string, *os.File, error) {
	for i := 1; i < 1000; i++ {
		name := base + ext
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return path, f, nil
		}
		if !os.IsExist(err) {
			return "", nil, fmt.Errorf("backup: %w", err)
		}
	}
	return "", nil, fmt.Errorf("backup: no free name for %s%s in %s", base, ext, dir)
}

// state is the size and modification time of one file, which is all that is
// needed to notice the game writing underneath the copy.
type state struct {
	size    int64
	modTime time.Time
}

func scan(root string) (map[string]state, error) {
	out := make(map[string]state)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			// A file that vanished between the walk and the stat is itself a
			// change; record it as such instead of failing the whole backup.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = state{size: info.Size(), modTime: info.ModTime()}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("backup: scanning %s: %w", root, err)
	}
	return out, nil
}

// diff names every file that appeared, vanished or moved between two scans.
func diff(before, after map[string]state) []string {
	var changed []string
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			changed = append(changed, name+" (removed)")
			continue
		}
		if a.size != b.size || !a.modTime.Equal(b.modTime) {
			changed = append(changed, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			changed = append(changed, name+" (added)")
		}
	}
	sort.Strings(changed)
	return changed
}

// openJournals names SQLite journals that held an uncommitted transaction.
//
// Project Zomboid keeps players.db and vehicles.db in rollback-journal mode and
// leaves a zero-length journal behind between transactions. A journal with
// bytes in it means the database was mid-write, and the copy of that database
// may not be loadable.
func openJournals(files map[string]state) []string {
	var open []string
	for name, st := range files {
		if strings.HasSuffix(name, "-journal") && st.size > 0 {
			open = append(open, name)
		}
	}
	sort.Strings(open)
	return open
}
