// Command pz-backup copies Project Zomboid saves into timestamped archives.
//
// It never overwrites an existing backup, and it reports whether the game was
// writing to the save while the copy ran -- the one thing that decides whether
// a backup taken from a live game can be trusted.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Legion112/pz-backup/internal/archive"
	"github.com/Legion112/pz-backup/internal/backup"
	"github.com/Legion112/pz-backup/internal/zomboid"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/pz-backup
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pz-backup: %v\n", err)
		os.Exit(1)
	}
}

const usage = `pz-backup <command> [flags]

  backup [--save ID] [--all] [--note TEXT] [--strict]
                    copy a save into a new archive (default: the save the game
                    last had open). Never overwrites an existing backup.
  list [--backups]  saves the game knows about, or archives already taken
  verify <archive>  read an archive end to end and check it against its manifest
  restore <archive> [--into DIR]
                    unpack an archive; refuses to overwrite anything

Flags:
  --zomboid DIR     the Zomboid user directory (default ~/Zomboid)
  --out DIR         where archives go (default ~/zomboid-backups)

Environment: PZ_ZOMBOID_DIR, PZ_BACKUP_DIR.
`

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "backup":
		return cmdBackup(args[1:])
	case "list":
		return cmdList(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "restore":
		return cmdRestore(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try: pz-backup help)", args[0])
	}
}

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	root := fs.String("zomboid", "", "Zomboid user directory")
	out := fs.String("out", "", "where archives go")
	save := fs.String("save", "", "save to back up, as Mode/Name or just Name")
	all := fs.Bool("all", false, "back up every save")
	note := fs.String("note", "", "a line recorded in the manifest, e.g. why you took this one")
	strict := fs.Bool("strict", false, "exit non-zero if the save changed while being copied")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir, err := zomboid.Open(*root)
	if err != nil {
		return err
	}

	var targets []zomboid.Save
	switch {
	case *all:
		targets, err = dir.Saves()
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return fmt.Errorf("no saves under %s", dir.SavesDir())
		}
	case *save != "":
		s, err := dir.Find(*save)
		if err != nil {
			return err
		}
		targets = []zomboid.Save{s}
	default:
		s, err := dir.Latest()
		if err != nil {
			return fmt.Errorf("%w (name one with --save, or use --all)", err)
		}
		targets = []zomboid.Save{s}
	}

	suspect := false
	for _, s := range targets {
		res, err := backup.Run(dir, s, backup.Options{
			OutDir:      *out,
			Note:        *note,
			ToolVersion: version,
		})
		if err != nil {
			return err
		}
		m := res.Manifest
		fmt.Printf("%s -> %s\n", m.Save, res.ArchivePath)
		fmt.Printf("  %d files, %s  ->  %s archive\n", m.Files, humanBytes(m.Bytes), humanBytes(m.ArchiveBytes))

		if m.Quiescent {
			fmt.Printf("  clean: nothing changed while copying\n")
		} else {
			suspect = true
			if n := len(m.ChangedDuringCopy); n > 0 {
				fmt.Printf("  SUSPECT: %d file(s) changed while copying: %s\n", n, preview(m.ChangedDuringCopy))
			}
			if n := len(m.OpenJournals); n > 0 {
				fmt.Printf("  SUSPECT: %d open SQLite journal(s): %s\n", n, preview(m.OpenJournals))
			}
			fmt.Printf("  the game was writing to this save. Quit to the main menu and run again\n")
			fmt.Printf("  for a copy you can rely on; this one is kept either way.\n")
		}
		if n := len(m.Skipped); n > 0 {
			fmt.Printf("  skipped %d non-regular file(s): %s\n", n, preview(m.Skipped))
		}
	}

	if suspect && *strict {
		return fmt.Errorf("at least one save was being written to (--strict)")
	}
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	root := fs.String("zomboid", "", "Zomboid user directory")
	out := fs.String("out", "", "where archives go")
	backups := fs.Bool("backups", false, "list archives already taken instead of saves")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer tw.Flush()

	if *backups {
		outDir := *out
		if outDir == "" {
			outDir = backup.DefaultOutDir()
		}
		entries, err := os.ReadDir(outDir)
		if err != nil {
			return fmt.Errorf("%w", err)
		}
		fmt.Fprintln(tw, "ARCHIVE\tSIZE\tTAKEN")
		found := false
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.zst") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			found = true
			fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name(), humanBytes(info.Size()), info.ModTime().Format("2006-01-02 15:04"))
		}
		if !found {
			fmt.Fprintf(tw, "(none in %s)\n", outDir)
		}
		return nil
	}

	dir, err := zomboid.Open(*root)
	if err != nil {
		return err
	}
	saves, err := dir.Saves()
	if err != nil {
		return err
	}
	latest, latestErr := dir.Latest()

	fmt.Fprintln(tw, "SAVE\tSIZE\tMODIFIED\t")
	for _, s := range saves {
		size, _ := dirSize(s.Path)
		info, err := os.Stat(s.Path)
		mod := ""
		if err == nil {
			mod = info.ModTime().Format("2006-01-02 15:04")
		}
		marker := ""
		if latestErr == nil && s.ID() == latest.ID() {
			marker = "<- last played"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID(), humanBytes(size), mod, marker)
	}
	return nil
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	out := fs.String("out", "", "where archives go")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("verify needs exactly one archive")
	}

	path, err := resolveArchive(fs.Arg(0), *out)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	stats, err := archive.Verify(f)
	if err != nil {
		return fmt.Errorf("%s is damaged: %w", filepath.Base(path), err)
	}
	fmt.Printf("%s\n", path)
	fmt.Printf("  reads clean: %d files, %d dirs, %s uncompressed\n", stats.Files, stats.Dirs, humanBytes(stats.Bytes))

	m, err := readManifest(path)
	if err != nil {
		fmt.Printf("  no manifest beside it (%v)\n", err)
		fmt.Printf("  without one this may be an interrupted run; the contents above still read clean.\n")
		return nil
	}
	if m.Files != stats.Files || m.Bytes != stats.Bytes {
		return fmt.Errorf("manifest says %d files / %s, archive holds %d files / %s",
			m.Files, humanBytes(m.Bytes), stats.Files, humanBytes(stats.Bytes))
	}
	fmt.Printf("  matches its manifest (%s, taken %s)\n", m.Save, m.Created.Format("2006-01-02 15:04"))
	if !m.Quiescent {
		fmt.Printf("  note: taken while the game was writing -- see changed_during_copy in the manifest\n")
	}
	if m.Note != "" {
		fmt.Printf("  note: %s\n", m.Note)
	}
	return nil
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	root := fs.String("zomboid", "", "Zomboid user directory")
	out := fs.String("out", "", "where archives go")
	into := fs.String("into", "", "unpack here instead of the Saves directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("restore needs exactly one archive")
	}

	path, err := resolveArchive(fs.Arg(0), *out)
	if err != nil {
		return err
	}

	dest := *into
	if dest == "" {
		dir, err := zomboid.Open(*root)
		if err != nil {
			return err
		}
		dest = dir.SavesDir()
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	stats, err := archive.Extract(f, dest)
	if err != nil {
		if os.IsExist(err) || strings.Contains(err.Error(), "file exists") {
			return fmt.Errorf("%w\n"+
				"nothing was overwritten. Move the existing save aside, or unpack\n"+
				"somewhere else with --into DIR and copy across what you need.", err)
		}
		return err
	}
	fmt.Printf("unpacked %d files (%s) into %s\n", stats.Files, humanBytes(stats.Bytes), dest)
	return nil
}

// resolveArchive accepts a path or a bare archive name in the backup directory.
func resolveArchive(name, outDir string) (string, error) {
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}
	if outDir == "" {
		outDir = backup.DefaultOutDir()
	}
	path := filepath.Join(outDir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("no archive %q, and none by that name in %s", name, outDir)
}

func readManifest(archivePath string) (backup.Manifest, error) {
	var m backup.Manifest
	p := strings.TrimSuffix(archivePath, ".tar.zst") + ".manifest.json"
	b, err := os.ReadFile(p)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// preview keeps a long list readable on one line.
func preview(items []string) string {
	const max = 3
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:max], ", "), len(items)-max)
}
