package migrate

import (
	"fmt"
	"strings"
)

type TemplateOptions struct {
	DisplayName  string
	Slug         string
	Description  string
	CPU          int
	RAMMiB       int
	Ethernet     int
	GuestOS      string
	Arch         string
	Firmware     string
	NICModel     string
	DiskBus      string
	TPMEnabled   bool
	HardwareUUID string
	QEMUVersion  string
	Icon         string
}

func RenderTemplateYAML(o TemplateOptions) string {
	stratumEngine := strings.EqualFold(strings.TrimSpace(o.QEMUVersion), "stratum")
	machine := "q35"
	if o.Arch == "aarch64" || o.Arch == "arm" {
		machine = "virt"
	}
	if o.HardwareUUID == "" {
		o.HardwareUUID = "auto"
	}
	accelerationValue := "auto"
	accelerationOptions := `  options: { auto: "Auto - use KVM when possible", kvm: "Require KVM", tcg: "Software Emulation" }`
	diskOptions := `  options: { virtio: "VirtIO Block", scsi: "VirtIO SCSI" }`
	archOptions := `  options: { x86_64: "x86-64", i386: "x86 32-bit", aarch64: "ARM64", arm: "ARM 32-bit" }`
	machineOptions := `  options: { q35: q35, pc: pc, virt: virt }`
	cpuModelValue := "auto"
	cpuModelOptions := `  options: { auto: "Auto", host: host, Penryn: Penryn, qemu64: qemu64, max: max }`
	firmwareOptions := `  options: { bios: "Legacy BIOS", uefi: "UEFI", secureboot: "UEFI Secure Boot" }`
	memoryEncryptionOptions := `  options: { off: "Off", amd-sev: "AMD SEV", amd-sev-es: "AMD SEV-ES", amd-sev-snp: "AMD SEV-SNP", intel-tdx: "Intel TDX" }`
	if stratumEngine {
		accelerationValue = "kvm"
		accelerationOptions = `  options: { kvm: "KVM - required by STRATUM" }`
		diskOptions = `  options: { virtio: "VirtIO Block", scsi: "VMBus SCSI (StorVSC)" }`
		archOptions = `  options: { x86_64: "x86-64", aarch64: "ARM64" }`
		machineOptions = fmt.Sprintf(`  options: { %s: %s }`, YAMLQuote(machine), YAMLQuote(machine))
		cpuModelValue = "host"
		cpuModelOptions = `  options: { host: host }`
		memoryEncryptionOptions = `  options: { off: "Off" }`
		if o.Arch == "x86_64" {
			firmwareOptions = `  options: { uefi: "UEFI", secureboot: "UEFI Secure Boot" }`
		} else {
			firmwareOptions = `  options: { uefi: "UEFI" }`
		}
	}
	lines := []string{
		fmt.Sprintf("name: %s", YAMLQuote(o.DisplayName)),
		fmt.Sprintf("description: %s", YAMLQuote(o.Description)),
		"type: qemu",
		fmt.Sprintf("icon: %s", o.Icon),
		fmt.Sprintf("slug: %s", YAMLQuote(o.Slug)),
		`palette_group: "systems"`,
		"",
		fmt.Sprintf("cpu: { type: number, value: %d }", maxInt(1, o.CPU)),
		fmt.Sprintf("ram: { type: number, value: %d }", maxInt(128, o.RAMMiB)),
		fmt.Sprintf("ethernet: { type: number, value: %d }", maxInt(0, o.Ethernet)),
		"",
		"# Physical GPU execution fields",
		"gpu_count: { type: number, value: 0, min: 0, max: 8 }",
		"gpu_type:",
		"  type: list",
		`  value: ""`,
		`  options: { "": "None" }`,
		"# STRATUM GPU Fabric fields",
		"stratum_gpu_count: { type: number, value: 0, min: 0, max: 8 }",
		"stratum_gpu_type:",
		"  type: list",
		`  value: ""`,
		`  options: { "": "None" }`,
		"stratum_gpu_mode:",
		"  type: list",
		`  value: "off"`,
		`  options: { native: "Native GPU Fabric", off: "Off" }`,
		"",
		"console:",
		"  type: list",
		`  value: "vnc"`,
		"  options: { telnet: Telnet, vnc: STRATUM }",
		"",
		"guest_os:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(o.GuestOS)),
		`  options: { "": "Auto-detect", linux: Linux, windows: Windows, other: Other }`,
		"guest_tools_media_id:",
		"  type: list",
		`  value: "auto"`,
		`  options: { auto: "Auto - match guest OS and architecture", none: None }`,
		"guest_tools_connect_at_power_on: { type: checkbox, value: 0 }",
		"",
		"qemu_acceleration:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(accelerationValue)),
		accelerationOptions,
		"#kvm:",
		"#  type: checkbox",
		"#  value: 1",
		"",
		"qemu_nic:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(o.NICModel)),
		`  options: { virtio: "VirtIO Network (virtio-net-pci)", vmxnet3: "VMware VMXNET3", e1000e: "Intel e1000e" }`,
		"",
		"disk_interface:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(o.DiskBus)),
		diskOptions,
		"",
		"qemu_arch:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(o.Arch)),
		archOptions,
		"qemu_machine:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(machine)),
		machineOptions,
		"qemu_cpu_model:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(cpuModelValue)),
		cpuModelOptions,
		"qemu_cpu_vmx: { type: checkbox, value: 0 }",
		"qemu_disable_hyperv_enlightenments: { type: checkbox, value: 0 }",
		"qemu_hide_hypervisor: { type: checkbox, value: 0 }",
		"firmware:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(o.Firmware)),
		firmwareOptions,
	}
	if o.TPMEnabled {
		lines = append(lines,
			"tpm:",
			"  type: list",
			"  value: tpm2",
			`  options: { disabled: "Disabled", tpm2: "TPM 2.0" }`,
		)
	}
	lines = append(lines,
		"memory_encryption_mode:",
		"  type: list",
		`  value: "off"`,
		memoryEncryptionOptions,
		fmt.Sprintf("hardware_uuid: %s", YAMLQuote(o.HardwareUUID)),
		"hardware_serial: auto",
		"",
		"qemu_version:",
		"  type: list",
		fmt.Sprintf("  value: %s", YAMLQuote(o.QEMUVersion)),
		"  options:",
		`    "stratum": "STRATUM"`,
		`    "canvas": "CANVAS"`,
		`    "canvas3d": "CANVAS 3D VirGL"`,
		"",
	)
	return strings.Join(lines, "\n")
}
