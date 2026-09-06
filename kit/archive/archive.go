// Package archive writes and reads tar.gz bundles.
//
// It exists rather than a direct call to archive/tar because of one thing:
// extraction guards. A tar entry named "../../etc/thing" is written wherever it
// says unless something checks, and most hand-rolled extractors do not check.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ubgo/lath/kit/scan"
)

const (
	// dirPerm is used for directories created during extraction. Entries carry
	// their own mode; this is only for parents an archive implies but omits.
	dirPerm = 0o755
)

// maxEntryBytes caps a single extracted file at 2 GiB. A decompression bomb
// otherwise fills the disk before anything notices.
//
// A var rather than a const solely so this package's own tests can lower it:
// verifying the cap by actually producing an oversized archive would mean
// writing two gigabytes on every test run.
var maxEntryBytes int64 = 2 << 30

// ErrUnsafePath reports an entry that would write outside the destination.
var ErrUnsafePath = errors.New("archive: entry escapes the destination")

// ErrEntryTooLarge reports an entry beyond maxEntryBytes.
var ErrEntryTooLarge = errors.New("archive: entry exceeds the size limit")

// TarGz writes the filtered tree under root to dst as a gzipped tar.
//
// Paths inside the archive are relative to root and slash-separated, so an
// archive written on one platform extracts correctly on another.
// ArchiveOption configures TarGz.
type ArchiveOption func(*archiveConfig)

type archiveConfig struct{ level int }

// CompressionLevel sets gzip's level, from gzip.NoCompression to
// gzip.BestCompression. Zero means gzip's default.
//
// Worth reaching: a bundle of already-compressed artifacts gains nothing from
// the default level and costs real seconds, while one shipped over a slow link
// wants the opposite.
func CompressionLevel(n int) ArchiveOption { return func(c *archiveConfig) { c.level = n } }

// TarGz writes root into dst as a gzipped tar, including only what f accepts.
//
// Why a Filter rather than a list of paths: the thing being archived is
// usually "a tree, minus the parts that do not belong", build output, vendor,
// .git, and expressing that as an exclusion is both shorter and correct as
// the tree grows. See scan.Filter.
//
// Invariant: paths inside the archive are RELATIVE to root, so extraction
// cannot depend on where it was created. Symlinks are stored as symlinks and
// never followed, so a link pointing outside root cannot smuggle its target
// into the archive.
//
// The named return exists so a failure to close the gzip writer, which is
// where a truncated archive comes from, is reported rather than lost.
func TarGz(dst, root string, f scan.Filter, opts ...ArchiveOption) (err error) {
	cfg := archiveConfig{level: gzip.DefaultCompression}
	for _, o := range opts {
		o(&cfg)
	}
	files, err := scan.Files(root, f)
	if err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("archive: creating %s: %w", dst, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("archive: closing %s: %w", dst, cerr)
		}
		if err != nil {
			_ = os.Remove(dst) // a failed archive must not look like a good one
		}
	}()

	gz, err := gzip.NewWriterLevel(out, cfg.level)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	tw := tar.NewWriter(gz)

	for _, path := range files {
		if err := addFile(tw, root, path); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("archive: finishing the tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("archive: finishing the gzip: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, root, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	// Symlinks are not archived. Writing one means the archive can carry an
	// escape, and Extract refuses them anyway, better to leave them out than
	// to write something that cannot be restored.
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	header.Name = filepath.ToSlash(rel)

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("archive: %s: %w", rel, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("archive: reading %s: %w", path, err)
	}
	return nil
}

type extractConfig struct {
	followSymlinks bool
	maxEntry       int64
}

// ExtractOption configures Extract.
type ExtractOption func(*extractConfig)

// FollowSymlinks materialises a symlink entry's target as a regular file
// instead of refusing it.
//
// Note what it does NOT do: no mode ever writes a link. A written symlink can
// point outside the destination no matter how its own path validates, so the
// two available behaviours are "refuse" and "copy the bytes", there is no
// unsafe third option to reach for under deadline.
func FollowSymlinks() ExtractOption { return func(c *extractConfig) { c.followSymlinks = true } }

// MaxEntryBytes caps a single extracted file, overriding maxEntryBytes.
//
// The default suits a deploy bundle. A caller extracting a genuinely large
// artifact should raise it deliberately rather than discover the limit as an
// unexplained failure, and one extracting untrusted input should lower it.
func MaxEntryBytes(n int64) ExtractOption { return func(c *extractConfig) { c.maxEntry = n } }

// Extract unpacks a tar.gz into dstDir, refusing any entry that would write
// outside it.
func Extract(ctx context.Context, src, dstDir string, opts ...ExtractOption) error {
	c := extractConfig{maxEntry: maxEntryBytes}
	for _, o := range opts {
		o(&c)
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("archive: %s is not gzip: %w", src, err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dstDir, dirPerm); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	// Resolved once so the containment check compares real paths; a symlinked
	// destination would otherwise make every check compare the wrong prefix.
	rootAbs, err := resolvedAbs(dstDir)
	if err != nil {
		return err
	}

	tr := tar.NewReader(gz)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("archive: reading %s: %w", src, err)
		}
		if err := extractOne(tr, header, rootAbs, c); err != nil {
			return err
		}
	}
}

func extractOne(tr *tar.Reader, header *tar.Header, rootAbs string, c extractConfig) error {
	target, err := safeJoin(rootAbs, header.Name)
	if err != nil {
		return err
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, dirPerm)

	case tar.TypeSymlink, tar.TypeLink:
		if !c.followSymlinks {
			return fmt.Errorf("archive: %s is a link: %w", header.Name, ErrUnsafePath)
		}
		// The link's TARGET must also be inside the destination, or following
		// it is the escape the refusal was preventing.
		source, err := safeJoin(filepath.Dir(target), header.Linkname)
		if err != nil {
			return err
		}
		return copyResolved(source, target, header)

	case tar.TypeReg:
		if header.Size > c.maxEntry {
			return fmt.Errorf("archive: %s is %d bytes: %w", header.Name, header.Size, ErrEntryTooLarge)
		}
		if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode())
		if err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		// LimitReader caps what a lying header can extract: Size is data, not
		// a promise, so the copy is bounded independently of it.
		_, copyErr := io.Copy(out, io.LimitReader(tr, c.maxEntry))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("archive: writing %s: %w", target, errors.Join(copyErr, closeErr))
		}
		return nil

	default:
		// Devices, FIFOs and sockets are silently skipped: a deploy bundle has
		// no business carrying them, and creating them is a privilege problem.
		return nil
	}
}

func copyResolved(source, target string, header *tar.Header) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("archive: following %s: %w", header.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err := os.WriteFile(target, data, header.FileInfo().Mode()); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return nil
}

// safeJoin resolves name under root and refuses anything that escapes.
//
// Checked with filepath.Rel rather than a string prefix: a prefix test says
// "/tmp/dst-evil" is inside "/tmp/dst", which it is not.
func safeJoin(root, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("archive: %q is absolute: %w", name, ErrUnsafePath)
	}
	joined := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive: %q: %w", name, ErrUnsafePath)
	}
	return joined, nil
}

// resolvedAbs returns dir as an absolute path with symlinks resolved.
func resolvedAbs(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("archive: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}
