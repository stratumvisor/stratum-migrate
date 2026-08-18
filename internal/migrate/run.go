package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Summary struct {
	Output       string
	TemplateName string
	ImageName    string
	Model        *OVFModel
	Result       *ConversionResult
	Firmware     string
	NICModel     string
	Arch         string
	TPMEnabled   bool
	Workdir      string
}

func Run(cfg Config, toolVersion string) (*Summary, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	source, err := filepath.Abs(cfg.Source)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("source does not exist: %s", source)
	}

	backend, virtV2V, qemuImg, err := selectBackend(cfg)
	if err != nil {
		return nil, err
	}
	version, err := NormalizeVersion(cfg.Version)
	if err != nil {
		return nil, err
	}

	workdir, err := os.MkdirTemp("", "stratum-migrate-")
	if err != nil {
		return nil, err
	}
	cleanup := !cfg.KeepWorkdir
	if cleanup {
		defer os.RemoveAll(workdir)
	}
	fmt.Fprintf(os.Stderr, "Working directory: %s\n", workdir)
	fmt.Fprintf(os.Stderr, "Conversion backend: %s\n", backend)

	applianceRoot := source
	if st.Mode().IsRegular() {
		if !strings.EqualFold(filepath.Ext(source), ".ova") {
			return nil, fmt.Errorf("source file must be an .ova; use a directory for an unpacked OVF")
		}
		applianceRoot = filepath.Join(workdir, "ova")
		mode := ExtractAll
		if backend == "virt-v2v" {
			mode = ExtractMetadataOnly
			fmt.Fprintf(os.Stderr, "Extracting OVA metadata for STRATUM bundle generation\n")
		} else {
			fmt.Fprintf(os.Stderr, "Extracting %s\n", filepath.Base(source))
		}
		if err := SafeExtractOVA(source, applianceRoot, mode); err != nil {
			return nil, err
		}
	} else if !st.IsDir() {
		return nil, fmt.Errorf("unsupported source type: %s", source)
	}

	descriptor, err := FindOVFDescriptor(applianceRoot, cfg.OVFDescriptor)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "OVF descriptor: %s\n", descriptor)
	if !cfg.SkipManifestCheck {
		if st.Mode().IsRegular() && backend == "virt-v2v" {
			fmt.Fprintf(os.Stderr, "OVF manifest verification: delegated to virt-v2v for the original OVA\n")
		} else {
			if err := VerifyOVFManifests(applianceRoot); err != nil {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "OVF manifest checksums: verified\n")
		}
	}

	model, err := ParseOVF(descriptor, cfg.VMSelector, backend == "qemu-img")
	if err != nil {
		return nil, err
	}
	templateDisplayName := valueOr(cfg.Name, model.Name)
	templateName := SlugifyName(templateDisplayName)
	if templateName == "template" && strings.TrimSpace(templateDisplayName) == "" {
		return nil, fmt.Errorf("unable to derive a usable STRATUM template name")
	}
	imageName := templateName + "-" + version
	output := cfg.Output
	if output == "" {
		cwd, _ := os.Getwd()
		output = filepath.Join(cwd, imageName+".stratumarsenal")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(output), ".stratumarsenal") {
		output += ".stratumarsenal"
	}
	if _, err := os.Stat(output); err == nil {
		if !cfg.Overwrite {
			return nil, fmt.Errorf("output already exists: %s; use --overwrite to replace it", output)
		}
		if err := os.Remove(output); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	templateDir := filepath.Join(workdir, "payload", "templates", templateName)
	imageDir := filepath.Join(workdir, "payload", "vm-images", imageName)
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return nil, err
	}

	var result *ConversionResult
	if backend == "virt-v2v" {
		inputPath := source
		if st.IsDir() {
			inputPath = filepath.Dir(descriptor)
		}
		result, err = RunVirtV2VBackend(cfg, model, imageDir, workdir, inputPath, virtV2V, qemuImg)
	} else {
		result, err = RunQEMUImgBackend(cfg, model, imageDir, workdir, qemuImg)
	}
	if err != nil {
		return nil, err
	}
	if len(result.Disks) == 0 {
		return nil, fmt.Errorf("conversion backend produced no disks")
	}

	if cfg.PreserveVMwareNVRAM && len(model.NVRAMFiles) > 0 {
		nvramDir := filepath.Join(imageDir, "migration-source", "vmware-nvram")
		for _, nvram := range model.NVRAMFiles {
			if err := CopyFile(nvram, filepath.Join(nvramDir, filepath.Base(nvram)), 0o600); err != nil {
				return nil, err
			}
		}
	}
	persistentV2VXML, persistentV2VLog := "", ""
	if backend == "virt-v2v" && cfg.PreserveV2VDiagnostics {
		diagDir := filepath.Join(imageDir, "migration-source", "virt-v2v")
		if result.V2VLibvirtXML != "" {
			persistentV2VXML = filepath.Join(diagDir, "converted-domain.xml")
			if err := CopyFile(result.V2VLibvirtXML, persistentV2VXML, 0o600); err != nil {
				return nil, err
			}
		}
		if result.V2VLog != "" {
			persistentV2VLog = filepath.Join(diagDir, "virt-v2v.log")
			if err := CopyFile(result.V2VLog, persistentV2VLog, 0o600); err != nil {
				return nil, err
			}
		}
	}

	firmware := result.Firmware
	if cfg.Firmware != "auto" {
		firmware = cfg.Firmware
	}
	arch := model.Arch
	if cfg.Arch != "auto" {
		arch = cfg.Arch
	}
	if (arch == "i386" || arch == "arm") && firmware != "bios" {
		model.Warnings = append(model.Warnings, fmt.Sprintf("forcing firmware=bios because STRATUM does not use UEFI for %s", arch))
		firmware = "bios"
	}
	nicModel := result.NICModel
	if cfg.NICModel != "auto" {
		nicModel = cfg.NICModel
	}
	// Arsenal bundles use the common NIC contract shared by CANVAS, CANVAS 3D,
	// and STRATUMVMM. Legacy source models remain valid migration inputs but
	// normalize before they become STRATUM runtime hardware.
	switch strings.ToLower(strings.TrimSpace(nicModel)) {
	case "virtio", "virtio-net-pci":
		nicModel = "virtio"
	case "vmxnet3":
		nicModel = "vmxnet3"
	case "e1000e":
		nicModel = "e1000e"
	case "e1000", "rtl8139", "":
		model.Warnings = append(model.Warnings, fmt.Sprintf("source NIC model %q is not in the portable STRATUM hardware contract; using e1000e", nicModel))
		nicModel = "e1000e"
	default:
		model.Warnings = append(model.Warnings, fmt.Sprintf("unrecognized source NIC model %q; using e1000e", nicModel))
		nicModel = "e1000e"
	}
	if result.Backend == "qemu-img" && model.GuestOS == "windows" {
		model.Warnings = append(model.Warnings, "qemu-img performs disk-format conversion only. The portable STRATUM disk interface is VirtIO Block/SCSI; an unmodified Windows guest may require VirtIO storage drivers before it will boot. Prefer --backend virt-v2v for Windows migrations.")
	}
	tpmEnabled := model.TPMPresent
	if cfg.TPM != "auto" {
		tpmEnabled = cfg.TPM == "tpm2"
	}
	description := valueOr(cfg.Description, model.Description)
	hardwareUUID := ""
	if cfg.Identity == "preserve" {
		hardwareUUID = model.HardwareUUID
	}
	canvas := RenderTemplateYAML(TemplateOptions{
		DisplayName: templateDisplayName, Slug: templateName, Description: description,
		CPU: model.CPU, RAMMiB: model.RAMMiB, Ethernet: model.Ethernet, GuestOS: model.GuestOS,
		Arch: arch, Firmware: firmware, NICModel: nicModel, DiskBus: result.Disks[0].OutputBus,
		TPMEnabled: tpmEnabled, HardwareUUID: hardwareUUID, QEMUVersion: cfg.QEMUVersion, Icon: cfg.Icon,
	})
	if err := os.WriteFile(filepath.Join(templateDir, "canvas.yml"), []byte(canvas), 0o640); err != nil {
		return nil, err
	}

	templateAsset, err := AssetManifest("templates", templateName, templateDir)
	if err != nil {
		return nil, err
	}
	imageAsset, err := AssetManifest("vm-images", imageName, imageDir)
	if err != nil {
		return nil, err
	}
	manifest := BundleManifest{
		Format: BundleFormat, Version: BundleVersion, Name: templateName, Description: description,
		CreatedAt: time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		Template:  &templateAsset, VMImages: []BundleAsset{imageAsset},
	}
	if err := CreateBundle(output, manifest, templateDir, imageDir, templateName, imageName); err != nil {
		return nil, err
	}
	if err := ValidateCreatedBundle(output); err != nil {
		return nil, err
	}

	if cfg.Report != "" {
		reportPath, err := filepath.Abs(cfg.Report)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			return nil, err
		}
		report := MigrationReport{
			Tool: "stratum-migrate", ToolVersion: toolVersion, BundleFormat: BundleFormat, BundleVersion: BundleVersion,
			Source: source, OVFDescriptor: descriptor, VirtualSystemID: model.VirtualSystemID, VirtualSystemName: model.Name,
			Output: output, TemplateName: templateName, ImageName: imageName, Backend: result.Backend,
			BackendVersion: result.BackendVersion, QEMUImgVersion: result.QEMUImgVersion,
			CPU: model.CPU, RAMMiB: model.RAMMiB, Ethernet: model.Ethernet, GuestOS: model.GuestOS,
			Architecture: arch, Firmware: firmware, NICModel: nicModel, TPMTemplateEnabled: tpmEnabled,
			IdentityPolicy: cfg.Identity, HardwareUUID: valueOr(hardwareUUID, "auto"), SourceMACAddresses: model.MACAddresses,
			VMwareNVRAMDetected: model.NVRAMFiles, VMwareNVRAMPreservedForAudit: cfg.PreserveVMwareNVRAM && len(model.NVRAMFiles) > 0,
			V2VCapabilities: result.V2VCapabilities, Warnings: model.Warnings,
		}
		if persistentV2VXML != "" {
			report.V2VLibvirtXML = "migration-source/virt-v2v/converted-domain.xml"
		}
		if persistentV2VLog != "" {
			report.V2VLog = "migration-source/virt-v2v/virt-v2v.log"
		}
		for _, disk := range result.Disks {
			report.Disks = append(report.Disks, map[string]any{
				"ovfDiskId": disk.OVFDiskID, "source": disk.Source, "sourceHref": disk.SourceHref,
				"sourceBus": disk.SourceBus, "sourceFormat": disk.SourceInfo.Format,
				"sourceVirtualSizeBytes": disk.SourceInfo.VirtualSize, "stratumBus": disk.OutputBus,
				"stratumFilename": disk.OutputName, "outputFormat": disk.OutputInfo.Format,
				"outputVirtualSizeBytes": disk.OutputInfo.VirtualSize, "outputActualSizeBytes": disk.OutputInfo.ActualSize,
			})
		}
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return nil, err
		}
		body = append(body, '\n')
		if err := os.WriteFile(reportPath, body, 0o640); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "Migration report: %s\n", reportPath)
	}

	if cfg.KeepWorkdir {
		fmt.Fprintf(os.Stderr, "Retained working directory: %s\n", workdir)
	}
	return &Summary{
		Output: output, TemplateName: templateName, ImageName: imageName, Model: model, Result: result,
		Firmware: firmware, NICModel: nicModel, Arch: arch, TPMEnabled: tpmEnabled, Workdir: workdir,
	}, nil
}

func selectBackend(cfg Config) (backend, virtV2V, qemuImg string, err error) {
	backend = cfg.Backend
	if backend == "auto" {
		if path, findErr := DiscoverVirtV2V(cfg.VirtV2V); findErr == nil {
			backend, virtV2V = "virt-v2v", path
		} else {
			backend = "qemu-img"
		}
	}
	if backend == "virt-v2v" {
		if virtV2V == "" {
			virtV2V, err = DiscoverVirtV2V(cfg.VirtV2V)
			if err != nil {
				return "", "", "", err
			}
		}
		qemuImg, err = tryDiscoverQEMUImg(cfg.QEMUImg)
		if err != nil {
			return "", "", "", err
		}
		return backend, virtV2V, qemuImg, nil
	}
	qemuImg, err = DiscoverQEMUImg(cfg.QEMUImg)
	if err != nil {
		return "", "", "", err
	}
	return "qemu-img", "", qemuImg, nil
}

func tryDiscoverQEMUImg(explicit string) (string, error) {
	path, err := DiscoverQEMUImg(explicit)
	if err == nil {
		return path, nil
	}
	if strings.TrimSpace(explicit) != "" || strings.TrimSpace(os.Getenv("STRATUM_QEMU_IMG")) != "" || strings.TrimSpace(os.Getenv("STRATUMD_QEMU_IMG_BINARY")) != "" {
		return "", err
	}
	return "", nil
}

func ValidateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Source) == "" {
		return fmt.Errorf("source OVA or OVF directory is required")
	}
	choices := map[string][]string{
		"backend":          {"auto", "qemu-img", "virt-v2v"},
		"disk-bus":         {"auto", "preserve", "scsi", "virtio"},
		"nic-model":        {"auto", "virtio", "vmxnet3", "e1000e"},
		"firmware":         {"auto", "bios", "uefi", "secureboot"},
		"tpm":              {"auto", "none", "tpm2"},
		"arch":             {"auto", "x86_64", "i386", "aarch64", "arm"},
		"identity":         {"preserve", "regenerate"},
		"qemu-version":     {"canvas", "canvas3d", "stratum"},
		"v2v-block-driver": {"virtio-blk", "virtio-scsi"},
	}
	values := map[string]string{
		"backend": cfg.Backend, "disk-bus": cfg.DiskBus, "nic-model": cfg.NICModel,
		"firmware": cfg.Firmware, "tpm": cfg.TPM, "arch": cfg.Arch, "identity": cfg.Identity,
		"qemu-version": cfg.QEMUVersion, "v2v-block-driver": cfg.V2VBlockDriver,
	}
	for key, allowed := range choices {
		value := values[key]
		valid := false
		for _, choice := range allowed {
			if value == choice {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid --%s %q; choose %s", key, value, strings.Join(allowed, ", "))
		}
	}
	if cfg.V2VParallel < 0 {
		return fmt.Errorf("--v2v-parallel may not be negative")
	}
	if cfg.V2VRoot != "ask" && cfg.V2VRoot != "single" && cfg.V2VRoot != "first" && !strings.HasPrefix(cfg.V2VRoot, "/dev/") {
		return fmt.Errorf("invalid --v2v-root %q; choose ask, single, first, or a /dev/... root device", cfg.V2VRoot)
	}
	return nil
}
