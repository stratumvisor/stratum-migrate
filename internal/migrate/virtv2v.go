package migrate

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type libvirtDomain struct {
	Name string `xml:"name"`
	OS   struct {
		Type struct {
			Arch    string `xml:"arch,attr"`
			Machine string `xml:"machine,attr"`
			Value   string `xml:",chardata"`
		} `xml:"type"`
		Loader struct {
			Secure string `xml:"secure,attr"`
			Value  string `xml:",chardata"`
		} `xml:"loader"`
		NVRAM string `xml:"nvram"`
	} `xml:"os"`
	Devices struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Driver struct {
				Type string `xml:"type,attr"`
			} `xml:"driver"`
			Source struct {
				File string `xml:"file,attr"`
				Dev  string `xml:"dev,attr"`
			} `xml:"source"`
			Target struct {
				Dev string `xml:"dev,attr"`
				Bus string `xml:"bus,attr"`
			} `xml:"target"`
		} `xml:"disk"`
		Interfaces []struct {
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
		} `xml:"interface"`
	} `xml:"devices"`
}

type v2vDisk struct {
	Path   string
	Bus    string
	Format string
	Dev    string
}

func DiscoverVirtV2V(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv("STRATUM_VIRT_V2V")
	}
	path, err := FindExecutable(explicit, "virt-v2v", "/usr/bin/virt-v2v", "/usr/local/bin/virt-v2v")
	if err != nil {
		return "", fmt.Errorf("virt-v2v was not found; install virt-v2v or force --backend qemu-img")
	}
	return path, nil
}

func VirtV2VCapabilities(binary string) []string {
	cmd := exec.Command(binary, "--machine-readable")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var capabilities []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			capabilities = append(capabilities, line)
		}
	}
	return capabilities
}

func RunVirtV2VBackend(cfg Config, model *OVFModel, imageDir, workdir, inputPath, virtV2V, qemuImg string) (*ConversionResult, error) {
	outDir := filepath.Join(workdir, "virt-v2v-output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(workdir, "virt-v2v.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	if err := validateV2VExtraArgs(cfg.V2VArgs); err != nil {
		return nil, err
	}
	capabilities := VirtV2VCapabilities(virtV2V)
	if len(capabilities) > 0 {
		if !containsString(capabilities, "input:ova") {
			return nil, fmt.Errorf("installed virt-v2v does not advertise input:ova support")
		}
		if !containsString(capabilities, "output:local") {
			return nil, fmt.Errorf("installed virt-v2v does not advertise output:local support")
		}
	}
	name := SlugifyName(valueOr(cfg.Name, model.Name))
	args := []string{"-i", "ova", inputPath}
	args = append(args, cfg.V2VArgs...)
	args = append(args,
		"-o", "local", "-os", outDir, "-of", "qcow2", "-oa", "sparse", "-on", name,
		"--root", cfg.V2VRoot, "--block-driver", cfg.V2VBlockDriver,
	)
	if cfg.V2VParallel > 0 {
		args = append(args, "--parallel", fmt.Sprintf("%d", cfg.V2VParallel))
	}
	if cfg.Quiet {
		args = append(args, "--quiet")
	}

	fmt.Fprintf(os.Stderr, "Running enterprise guest conversion with virt-v2v\n")
	fmt.Fprintf(logFile, "Command: %s %s\n\n", virtV2V, strings.Join(args, " "))
	cmd := exec.Command(virtV2V, args...)
	cmd.Env = os.Environ()
	if strings.TrimSpace(cfg.V2VTmpDir) != "" {
		cmd.Env = append(cmd.Env, "VIRT_V2V_TMPDIR="+cfg.V2VTmpDir)
	}
	if cfg.Quiet {
		cmd.Stdout, cmd.Stderr = logFile, logFile
	} else {
		writer := io.MultiWriter(os.Stderr, logFile)
		cmd.Stdout, cmd.Stderr = writer, writer
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("virt-v2v conversion failed: %w (log: %s)", err, logPath)
	}
	if err := logFile.Sync(); err != nil {
		return nil, err
	}

	xmlPath, domain, disks, err := readVirtV2VOutput(outDir, name)
	if err != nil {
		return nil, err
	}
	result := &ConversionResult{
		Backend: "virt-v2v", BackendVersion: CommandVersion(virtV2V), QEMUImgVersion: CommandVersion(qemuImg),
		Firmware: model.Firmware, NICModel: model.NICModel, V2VLibvirtXML: xmlPath, V2VLog: logPath,
		V2VCapabilities: capabilities,
	}
	if strings.TrimSpace(domain.OS.Loader.Value) != "" || strings.TrimSpace(domain.OS.NVRAM) != "" {
		result.Firmware = "uefi"
		if boolFromText(domain.OS.Loader.Secure) {
			result.Firmware = "secureboot"
		}
	}
	if len(domain.Devices.Interfaces) > 0 {
		modelType := strings.ToLower(strings.TrimSpace(domain.Devices.Interfaces[0].Model.Type))
		switch modelType {
		case "virtio", "virtio-net-pci":
			result.NICModel = "virtio"
		case "vmxnet3", "e1000e":
			result.NICModel = modelType
		case "e1000", "rtl8139":
			result.NICModel = "e1000e"
		}
	}

	busCounts := map[string]int{}
	for i, disk := range disks {
		sourceBus, sourcePath, sourceHref, ovfID := "unknown", "", "", ""
		if i < len(model.AttachedDisks) {
			sourceBus = model.AttachedDisks[i].SourceBus
			sourcePath = model.AttachedDisks[i].Source
			sourceHref = model.AttachedDisks[i].SourceHref
			ovfID = model.AttachedDisks[i].DiskID
		}
		bus := ChooseOutputBus(sourceBus, cfg.DiskBus, disk.Bus)
		name, err := DiskFilename(bus, busCounts[bus])
		if err != nil {
			return nil, err
		}
		busCounts[bus]++
		destination := filepath.Join(imageDir, name)
		if err := MoveFile(disk.Path, destination, 0o640); err != nil {
			return nil, fmt.Errorf("move virt-v2v disk %s: %w", filepath.Base(disk.Path), err)
		}
		outputInfo := DiskInfo{Format: valueOr(disk.Format, "qcow2")}
		if st, err := os.Stat(destination); err == nil {
			outputInfo.ActualSize = st.Size()
		}
		if qemuImg != "" {
			if err := QEMUImgCheck(qemuImg, destination); err != nil {
				return nil, err
			}
			if info, err := QEMUImgInfo(qemuImg, destination); err == nil {
				outputInfo = info
			}
		}
		result.Disks = append(result.Disks, ConvertedDisk{
			OVFDiskID: ovfID, Source: sourcePath, SourceHref: sourceHref, SourceBus: sourceBus,
			OutputBus: bus, OutputName: name, OutputPath: destination, OutputInfo: outputInfo,
		})
	}
	return result, nil
}

func readVirtV2VOutput(outDir, expectedName string) (string, *libvirtDomain, []v2vDisk, error) {
	xmlPath := filepath.Join(outDir, expectedName+".xml")
	if _, err := os.Stat(xmlPath); err != nil {
		matches, _ := filepath.Glob(filepath.Join(outDir, "*.xml"))
		sort.Strings(matches)
		if len(matches) != 1 {
			return "", nil, nil, fmt.Errorf("virt-v2v output did not contain exactly one libvirt XML descriptor under %s", outDir)
		}
		xmlPath = matches[0]
	}
	body, err := os.ReadFile(xmlPath)
	if err != nil {
		return "", nil, nil, err
	}
	var domain libvirtDomain
	if err := xml.Unmarshal(body, &domain); err != nil {
		return "", nil, nil, fmt.Errorf("parse virt-v2v libvirt XML: %w", err)
	}
	var disks []v2vDisk
	for _, item := range domain.Devices.Disks {
		if item.Device != "" && item.Device != "disk" {
			continue
		}
		path := valueOr(item.Source.File, item.Source.Dev)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(outDir, path)
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() {
			return "", nil, nil, fmt.Errorf("virt-v2v XML references missing converted disk: %s", path)
		}
		disks = append(disks, v2vDisk{Path: path, Bus: item.Target.Bus, Format: item.Driver.Type, Dev: item.Target.Dev})
	}
	if len(disks) == 0 {
		return "", nil, nil, fmt.Errorf("virt-v2v produced no converted disks in %s", xmlPath)
	}
	return xmlPath, &domain, disks, nil
}

func validateV2VExtraArgs(args []string) error {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		for _, forbidden := range []string{"-i", "-o", "-os", "-of", "-oa", "-on", "--in-place", "--no-copy", "--root", "--block-driver", "--parallel"} {
			if trimmed == forbidden || strings.HasPrefix(trimmed, forbidden+"=") {
				return fmt.Errorf("--virt-v2v-arg may not override managed option %s", forbidden)
			}
		}
	}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
