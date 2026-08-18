package migrate

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func AssetManifest(rootKey, name, path string) (BundleAsset, error) {
	size, checksum, err := DirSummary(path)
	if err != nil {
		return BundleAsset{}, err
	}
	return BundleAsset{
		Root: rootKey, Name: name, LogicalPath: "/" + name,
		ArchivePath: "payload/" + rootKey + "/" + name,
		ItemType:    "folder", SHA256: checksum, SizeBytes: size,
	}, nil
}

func CreateBundle(output string, manifest BundleManifest, templateDir, imageDir, templateName, imageName string) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	partial := output + ".partial"
	_ = os.Remove(partial)
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	cleanup := func(cause error) error {
		_ = zw.Close()
		_ = f.Close()
		_ = os.Remove(partial)
		return cause
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return cleanup(err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := addBytesToZip(zw, "manifest.json", manifestBytes, 0o644, zip.Deflate); err != nil {
		return cleanup(err)
	}
	if err := addDirectoryToZip(zw, templateDir, "payload/templates/"+templateName); err != nil {
		return cleanup(err)
	}
	if err := addDirectoryToZip(zw, imageDir, "payload/vm-images/"+imageName); err != nil {
		return cleanup(err)
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, output); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return nil
}

func addBytesToZip(zw *zip.Writer, name string, body []byte, mode os.FileMode, method uint16) error {
	h := &zip.FileHeader{Name: filepath.ToSlash(name), Method: method, Modified: time.Now()}
	h.SetMode(mode)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func addDirectoryToZip(zw *zip.Writer, source, archiveRoot string) error {
	archiveRoot = strings.TrimSuffix(filepath.ToSlash(archiveRoot), "/")
	if err := addZipDirectory(zw, archiveRoot+"/", 0o755); err != nil {
		return err
	}
	var paths []string
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported in Arsenal bundles: %s", path)
		}
		if path != source {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(paths, func(i, j int) bool {
		ri, _ := filepath.Rel(source, paths[i])
		rj, _ := filepath.Rel(source, paths[j])
		return filepath.ToSlash(ri) < filepath.ToSlash(rj)
	})
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := archiveRoot + "/" + filepath.ToSlash(rel)
		if info.IsDir() {
			if err := addZipDirectory(zw, name+"/", info.Mode().Perm()); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported filesystem object in bundle: %s", path)
		}
		h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
		h.SetMode(info.Mode().Perm())
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(w, f, make([]byte, 8*1024*1024))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func addZipDirectory(zw *zip.Writer, name string, mode os.FileMode) error {
	h := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Store, Modified: time.Now()}
	h.SetMode(os.ModeDir | mode)
	_, err := zw.CreateHeader(h)
	return err
}

func ValidateCreatedBundle(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	var manifest BundleManifest
	found := false
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name != "manifest.json" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		decErr := json.NewDecoder(r).Decode(&manifest)
		closeErr := r.Close()
		if decErr != nil {
			return fmt.Errorf("created bundle has invalid manifest.json: %w", decErr)
		}
		if closeErr != nil {
			return closeErr
		}
		found = true
	}
	if !found {
		return fmt.Errorf("created bundle has no manifest.json")
	}
	if manifest.Format != BundleFormat || manifest.Version != BundleVersion {
		return fmt.Errorf("created bundle manifest has an unexpected format/version")
	}
	var assets []BundleAsset
	if manifest.Template != nil {
		assets = append(assets, *manifest.Template)
	}
	assets = append(assets, manifest.VMImages...)
	if len(assets) == 0 {
		return fmt.Errorf("created bundle contains no assets")
	}
	for _, asset := range assets {
		prefix := strings.TrimSuffix(asset.ArchivePath, "/") + "/"
		present := false
		for name := range names {
			if name == prefix || strings.HasPrefix(name, prefix) {
				present = true
				break
			}
		}
		if !present {
			return fmt.Errorf("created bundle is missing payload for %s", asset.Name)
		}
	}
	return nil
}
