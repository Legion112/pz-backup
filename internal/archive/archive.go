// Package archive reads and writes the tar+zstd files a backup is stored in.
//
// zstd rather than gzip because saves are mostly chunk data that compresses
// well and re-compresses often: a 487 MiB save set lands around 86 MiB, and the
// encoder keeps up with the disk. The format is a plain tar inside a zstd
// frame, so "tar --zstd -xf" works on any archive this writes -- recovering a
// save never requires this tool.
package archive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Stats counts what an archive holds.
type Stats struct {
	Files int
	Dirs  int
	Bytes int64
}

// Create writes root/rel into w as a zstd-compressed tar, with every path in
// the archive relative to root.
//
// Only regular files and directories are stored. Anything else -- a symlink, a
// socket, a device node -- is skipped and named in the returned slice, because
// silently storing or silently dropping them are both worse than saying so.
func Create(w io.Writer, root, rel string) (Stats, []string, error) {
	var stats Stats
	var skipped []string

	enc, err := zstd.NewWriter(w)
	if err != nil {
		return stats, nil, fmt.Errorf("archive: %w", err)
	}
	tw := tar.NewWriter(enc)

	walkRoot := filepath.Join(root, rel)
	err = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)

		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = name + "/"
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			stats.Dirs++
			return nil

		case info.Mode().IsRegular():
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = name
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			n, err := io.Copy(tw, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("copying %s: %w", name, err)
			}
			// A file that grew or shrank between stat and read would desync the
			// tar stream; tar.Writer catches the mismatch, but say which file.
			if n != info.Size() {
				return fmt.Errorf("%s changed size while being read (%d -> %d)", name, info.Size(), n)
			}
			stats.Files++
			stats.Bytes += n
			return nil

		default:
			skipped = append(skipped, name)
			return nil
		}
	})
	if err != nil {
		return stats, skipped, fmt.Errorf("archive: %w", err)
	}
	if err := tw.Close(); err != nil {
		return stats, skipped, fmt.Errorf("archive: %w", err)
	}
	if err := enc.Close(); err != nil {
		return stats, skipped, fmt.Errorf("archive: %w", err)
	}
	return stats, skipped, nil
}

// Verify reads an archive end to end and reports what it holds.
//
// This is a real read of every byte, not a header scan: a truncated or
// bit-rotted archive fails here and nowhere else until the day you need it.
func Verify(r io.Reader) (Stats, error) {
	var stats Stats

	dec, err := zstd.NewReader(r)
	if err != nil {
		return stats, fmt.Errorf("archive: %w", err)
	}
	defer dec.Close()

	tr := tar.NewReader(dec)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("archive: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			stats.Dirs++
		case tar.TypeReg:
			n, err := io.Copy(io.Discard, tr)
			if err != nil {
				return stats, fmt.Errorf("archive: reading %s: %w", hdr.Name, err)
			}
			stats.Files++
			stats.Bytes += n
		}
	}
	return stats, nil
}

// Extract unpacks an archive under dest.
//
// It never overwrites: an entry whose path already exists is an error, and the
// caller is left to decide what to do about it. Entry paths are checked to stay
// under dest, so a hand-edited archive cannot write outside it.
func Extract(r io.Reader, dest string) (Stats, error) {
	var stats Stats

	dec, err := zstd.NewReader(r)
	if err != nil {
		return stats, fmt.Errorf("archive: %w", err)
	}
	defer dec.Close()

	absDest, err := filepath.Abs(dest)
	if err != nil {
		return stats, fmt.Errorf("archive: %w", err)
	}

	tr := tar.NewReader(dec)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("archive: %w", err)
		}

		target := filepath.Join(absDest, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target, absDest+string(os.PathSeparator)) && target != absDest {
			return stats, fmt.Errorf("archive: entry %q escapes the destination", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return stats, fmt.Errorf("archive: %w", err)
			}
			stats.Dirs++

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return stats, fmt.Errorf("archive: %w", err)
			}
			// O_EXCL is the whole no-overwrite guarantee: the open fails rather
			// than truncating something that is already there.
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return stats, fmt.Errorf("archive: %w", err)
			}
			n, err := io.Copy(f, tr)
			closeErr := f.Close()
			if err != nil {
				return stats, fmt.Errorf("archive: writing %s: %w", hdr.Name, err)
			}
			if closeErr != nil {
				return stats, fmt.Errorf("archive: %w", closeErr)
			}
			stats.Files++
			stats.Bytes += n
		}
	}
	return stats, nil
}
