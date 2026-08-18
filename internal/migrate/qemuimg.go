package migrate

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func DiscoverQEMUImg(explicit string) (string, error) {
	if explicit == "" {
		explicit = firstNonEmpty(os.Getenv("STRATUM_QEMU_IMG"), os.Getenv("STRATUMD_QEMU_IMG_BINARY"))
	}
	path, err := FindExecutable(explicit,
		"stratumimg", "qemu-img", "/stratum/qemu/bin/qemu-img", "/usr/bin/qemu-img", "/usr/local/bin/qemu-img")
	if err != nil {
		return "", fmt.Errorf("qemu-img was not found; install qemu-img, run inside STRATUM, or pass --qemu-img /path/to/qemu-img")
	}
	return path, nil
}

func PrepareCompressedSource(source, compression, workdir string) (string, error) {
	compression = strings.ToLower(strings.TrimSpace(compression))
	if compression == "" || compression == "identity" {
		return source, nil
	}
	if compression != "gzip" && compression != "gz" {
		return "", fmt.Errorf("unsupported OVF file compression %q for %s", compression, filepath.Base(source))
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(source)
	if strings.HasSuffix(strings.ToLower(name), ".gz") {
		name = name[:len(name)-3]
	} else {
		name += ".decompressed"
	}
	output := filepath.Join(workdir, name)
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	out, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	_, copyErr := io.CopyBuffer(out, gz, make([]byte, 8*1024*1024))
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return output, nil
}

func RunQEMUImgBackend(cfg Config, model *OVFModel, imageDir, workdir, qemuImg string) (*ConversionResult, error) {
	result := &ConversionResult{
		Backend: "qemu-img", BackendVersion: CommandVersion(qemuImg), QEMUImgVersion: CommandVersion(qemuImg),
		Firmware: model.Firmware, NICModel: model.NICModel,
	}
	busCounts := map[string]int{}
	for _, disk := range model.AttachedDisks {
		bus := ChooseOutputBus(disk.SourceBus, cfg.DiskBus, "")
		name, err := DiskFilename(bus, busCounts[bus])
		if err != nil {
			return nil, err
		}
		busCounts[bus]++
		destination := filepath.Join(imageDir, name)
		source := disk.Source
		if strings.TrimSpace(disk.Compression) != "" {
			source, err = PrepareCompressedSource(source, disk.Compression, filepath.Join(workdir, "decompressed"))
			if err != nil {
				return nil, err
			}
		}
		fmt.Fprintf(os.Stderr, "Converting %s -> %s\n", filepath.Base(source), name)
		sourceInfo, outputInfo, err := ConvertWithQEMUImg(qemuImg, source, destination, cfg.QCOW2Options, cfg.Quiet)
		if err != nil {
			return nil, err
		}
		result.Disks = append(result.Disks, ConvertedDisk{
			OVFDiskID: disk.DiskID, Source: disk.Source, SourceHref: disk.SourceHref, SourceBus: disk.SourceBus,
			OutputBus: bus, OutputName: name, OutputPath: destination, SourceInfo: sourceInfo, OutputInfo: outputInfo,
		})
	}
	return result, nil
}

func ConvertWithQEMUImg(binary, source, destination, qcow2Options string, quiet bool) (DiskInfo, DiskInfo, error) {
	sourceInfo, err := QEMUImgInfo(binary, source)
	if err != nil {
		return DiskInfo{}, DiskInfo{}, err
	}
	allowed := map[string]bool{"vmdk": true, "raw": true, "qcow2": true, "vdi": true, "vhdx": true, "vpc": true, "qed": true}
	if !allowed[strings.ToLower(sourceInfo.Format)] {
		return DiskInfo{}, DiskInfo{}, fmt.Errorf("unsupported or unrecognized source disk format %q for %s", sourceInfo.Format, filepath.Base(source))
	}
	partial := destination + ".partial"
	_ = os.Remove(partial)
	args := []string{"convert"}
	if !quiet {
		args = append(args, "-p")
	}
	args = append(args, "-f", strings.ToLower(sourceInfo.Format), "-O", "qcow2")
	if strings.TrimSpace(qcow2Options) != "" {
		args = append(args, "-o", qcow2Options)
	}
	args = append(args, source, partial)
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(partial)
		return DiskInfo{}, DiskInfo{}, fmt.Errorf("qemu-img convert failed for %s: %w", filepath.Base(source), err)
	}
	if err := QEMUImgCheck(binary, partial); err != nil {
		_ = os.Remove(partial)
		return DiskInfo{}, DiskInfo{}, err
	}
	outputInfo, err := QEMUImgInfo(binary, partial)
	if err != nil {
		_ = os.Remove(partial)
		return DiskInfo{}, DiskInfo{}, err
	}
	if strings.ToLower(outputInfo.Format) != "qcow2" {
		_ = os.Remove(partial)
		return DiskInfo{}, DiskInfo{}, fmt.Errorf("converted output is not qcow2: %s", filepath.Base(destination))
	}
	if err := os.Chmod(partial, 0o640); err != nil {
		_ = os.Remove(partial)
		return DiskInfo{}, DiskInfo{}, err
	}
	if err := os.Rename(partial, destination); err != nil {
		_ = os.Remove(partial)
		return DiskInfo{}, DiskInfo{}, err
	}
	return sourceInfo, outputInfo, nil
}

func QEMUImgInfo(binary, path string) (DiskInfo, error) {
	cmd := exec.Command(binary, "info", "--output=json", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return DiskInfo{}, fmt.Errorf("qemu-img info failed for %s: %s", filepath.Base(path), strings.TrimSpace(string(out)))
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return DiskInfo{}, fmt.Errorf("qemu-img returned invalid JSON for %s: %w", filepath.Base(path), err)
	}
	return DiskInfo{
		Format: stringValue(raw["format"]), VirtualSize: int64Value(raw["virtual-size"]), ActualSize: int64Value(raw["actual-size"]),
	}, nil
}

func QEMUImgCheck(binary, path string) error {
	cmd := exec.Command(binary, "check", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img check failed for %s: %s", filepath.Base(path), strings.TrimSpace(string(out)))
	}
	return nil
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func int64Value(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
