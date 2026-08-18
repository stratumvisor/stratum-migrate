package migrate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ExtractMode int

const (
	ExtractAll ExtractMode = iota
	ExtractMetadataOnly
)

func SafeExtractOVA(ovaPath, destination string, mode ExtractMode) error {
	f, err := os.Open(ovaPath)
	if err != nil {
		return fmt.Errorf("open OVA: %w", err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	var r io.Reader = reader
	magic, _ := reader.Peek(2)
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return fmt.Errorf("open gzip-compressed OVA: %w", err)
		}
		defer gz.Close()
		r = gz
	}

	base, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read OVA archive: %w", err)
		}
		name := strings.ReplaceAll(hdr.Name, `\`, "/")
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." {
			continue
		}
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe OVA member path: %s", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink || hdr.Typeflag == tar.TypeChar || hdr.Typeflag == tar.TypeBlock || hdr.Typeflag == tar.TypeFifo {
			return fmt.Errorf("unsupported OVA member type: %s", hdr.Name)
		}
		if mode == ExtractMetadataOnly && !isOVFMetadataPath(clean) && hdr.Typeflag != tar.TypeDir {
			continue
		}
		target := filepath.Join(base, clean)
		rel, err := filepath.Rel(base, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("OVA member escapes extraction directory: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if mode == ExtractAll || shouldCreateMetadataDir(clean) {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			perm := os.FileMode(hdr.Mode) & 0o777
			if perm == 0 {
				perm = 0o640
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyBuffer(out, tr, make([]byte, 8*1024*1024))
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			// PAX and GNU metadata records are handled by archive/tar. Ignore other
			// metadata-only records, but never materialize special files.
		}
	}
	return nil
}

func isOVFMetadataPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ovf", ".mf", ".nvram", ".cert", ".pem":
		return true
	default:
		return false
	}
}

func shouldCreateMetadataDir(path string) bool {
	// Directories are cheap and preserve nested descriptor locations. Empty
	// directories do not affect the final bundle because this extraction tree is
	// only a working area.
	return path != "."
}
