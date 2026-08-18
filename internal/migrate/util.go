package migrate

import (
	"bufio"
	"crypto/sha1" // #nosec G505 -- required for validating legacy OVF manifests.
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`[^A-Za-z0-9._+\-]+`)

func SlugifyName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "template"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "template"
	}
	return out
}

func NormalizeVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "1.0.0", nil
	}
	value = strings.Join(strings.Fields(value), "-")
	value = strings.Trim(versionPattern.ReplaceAllString(value, "-"), "-.")
	if value == "" {
		return "", errors.New("version/tag becomes empty after normalization")
	}
	if strings.ContainsAny(value, `/\\`) {
		return "", errors.New("version/tag may not contain path separators")
	}
	return value, nil
}

func YAMLQuote(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func alphaSuffix(index int) string {
	if index < 0 {
		index = 0
	}
	value := index + 1
	out := ""
	for value > 0 {
		var rem int
		value, rem = (value-1)/26, (value-1)%26
		out = string(rune('a'+rem)) + out
	}
	return out
}

func DiskFilename(bus string, index int) (string, error) {
	suffix := alphaSuffix(index)
	switch bus {
	case "virtio":
		return "virtio" + suffix + ".qcow2", nil
	case "scsi":
		return "sd" + suffix + ".qcow2", nil
	default:
		return "", fmt.Errorf("unsupported STRATUM disk bus: %s", bus)
	}
}

func ChooseOutputBus(sourceBus, policy, backendBus string) string {
	sourceBus = strings.ToLower(sourceBus)
	backendBus = normalizeDiskBus(backendBus)
	switch policy {
	case "scsi", "virtio":
		return policy
	case "preserve":
		// STRATUM's portable disk contract is VirtIO Block or VirtIO SCSI.
		// Preserve only controllers that map directly to that contract; foreign
		// SATA/IDE/LSI-style disks normalize to virtio-scsi.
		if sourceBus == "virtio" || sourceBus == "scsi" {
			return sourceBus
		}
		return "scsi"
	}
	if backendBus != "" {
		return backendBus
	}
	// qemu-img does format conversion only and cannot preserve VMware/AHCI/IDE
	// hardware. Use the common virtio-scsi contract by default.
	return "scsi"
}

func normalizeDiskBus(bus string) string {
	switch strings.ToLower(strings.TrimSpace(bus)) {
	case "virtio", "virtio-blk":
		return "virtio"
	case "scsi", "virtio-scsi":
		return "scsi"
	case "sata", "ahci", "ide":
		return "scsi"
	default:
		return ""
	}
}

func FindExecutable(explicit string, candidates ...string) (string, error) {
	all := make([]string, 0, len(candidates)+1)
	if strings.TrimSpace(explicit) != "" {
		all = append(all, explicit)
	}
	all = append(all, candidates...)
	seen := map[string]bool{}
	for _, candidate := range all {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		resolved := candidate
		if !strings.ContainsRune(candidate, os.PathSeparator) {
			if path, err := exec.LookPath(candidate); err == nil {
				resolved = path
			} else {
				continue
			}
		}
		st, err := os.Stat(resolved)
		if err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			abs, err := filepath.Abs(resolved)
			if err == nil {
				return abs, nil
			}
			return resolved, nil
		}
	}
	return "", errors.New("executable not found")
}

func CommandVersion(binary string) string {
	if binary == "" {
		return ""
	}
	for _, args := range [][]string{{"--version"}, {"-V"}, {"version"}} {
		cmd := exec.Command(binary, args...)
		out, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			return line
		}
	}
	return ""
}

func SecureResolve(base, href string) (string, error) {
	parsed, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("invalid OVF file reference %q: %w", href, err)
	}
	if parsed.Scheme != "" && parsed.Scheme != "file" {
		return "", fmt.Errorf("remote OVF file reference is not supported: %s", href)
	}
	decoded := href
	if parsed.Scheme == "file" {
		decoded = parsed.Path
	}
	decoded, err = url.PathUnescape(decoded)
	if err != nil {
		return "", fmt.Errorf("invalid escaped OVF path %q: %w", href, err)
	}
	decoded = strings.ReplaceAll(decoded, `\`, "/")
	if strings.HasPrefix(decoded, "/") {
		return "", fmt.Errorf("unsafe absolute OVF file reference: %s", href)
	}
	clean := filepath.Clean(filepath.FromSlash(decoded))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe OVF file reference: %s", href)
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	candidate, err := filepath.Abs(filepath.Join(baseAbs, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("OVF file reference escapes appliance directory: %s", href)
	}
	return candidate, nil
}

func CopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".partial"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(out, in, make([]byte, 8*1024*1024))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func MoveFile(src, dst string, mode os.FileMode) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return os.Chmod(dst, mode)
	}
	if err := CopyFile(src, dst, mode); err != nil {
		return err
	}
	return os.Remove(src)
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 8*1024*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func HashFile(path, algorithm string) (string, error) {
	var h hash.Hash
	switch strings.ToUpper(algorithm) {
	case "SHA1":
		h = sha1.New() // #nosec G401 -- verifying source manifest only.
	case "SHA256":
		h = sha256.New()
	case "SHA512":
		h = sha512.New()
	default:
		return "", fmt.Errorf("unsupported hash algorithm %s", algorithm)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.CopyBuffer(h, f, make([]byte, 8*1024*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func DirSummary(root string) (int64, string, error) {
	h := sha256.New()
	var total int64
	err := filepath.Walk(root, func(curr string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported in Arsenal assets: %s", curr)
		}
		rel, err := filepath.Rel(root, curr)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		_, _ = io.WriteString(h, rel)
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported filesystem object in Arsenal asset: %s", curr)
		}
		total += info.Size()
		fh, err := FileSHA256(curr)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, fh)
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	return total, hex.EncodeToString(h.Sum(nil)), nil
}

func NaturalLess(a, b string) bool {
	// Good enough for predictable disk and descriptor ordering.
	re := regexp.MustCompile(`(\d+|\D+)`)
	ap := re.FindAllString(a, -1)
	bp := re.FindAllString(b, -1)
	for i := 0; i < len(ap) && i < len(bp); i++ {
		ai, aerr := strconv.Atoi(ap[i])
		bi, berr := strconv.Atoi(bp[i])
		if aerr == nil && berr == nil {
			if ai != bi {
				return ai < bi
			}
			continue
		}
		al, bl := strings.ToLower(ap[i]), strings.ToLower(bp[i])
		if al != bl {
			return al < bl
		}
	}
	return len(ap) < len(bp)
}

func SortedWalkFiles(root string, suffix string) ([]string, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not supported: %s", path)
		}
		if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(path), suffix) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Slice(paths, func(i, j int) bool { return NaturalLess(paths[i], paths[j]) })
	return paths, err
}

func FirstLine(value string) string {
	s := bufio.NewScanner(strings.NewReader(value))
	if s.Scan() {
		return strings.TrimSpace(s.Text())
	}
	return strings.TrimSpace(value)
}

func FormatBytes(value int64) string {
	if value < 0 {
		return "unknown"
	}
	amount := float64(value)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	for i, unit := range units {
		if amount < 1024 || i == len(units)-1 {
			if unit == "B" {
				return fmt.Sprintf("%d B", value)
			}
			return fmt.Sprintf("%.1f %s", amount, unit)
		}
		amount /= 1024
	}
	return fmt.Sprintf("%d B", value)
}
