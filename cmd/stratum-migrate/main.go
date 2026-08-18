package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"stratum-migrate/internal/migrate"
)

var version = "dev"

type stringSliceFlag []string

func (s *stringSliceFlag) String() string         { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(value string) error { *s = append(*s, value); return nil }

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	for _, arg := range args {
		if arg == "-V" || arg == "--tool-version" {
			fmt.Printf("stratum-migrate %s\n", version)
			return 0
		}
	}
	cfg := migrate.Config{}
	var v2vArgs stringSliceFlag
	fs := flag.NewFlagSet("stratum-migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { printUsage(fs) }
	fs.StringVar(&cfg.Output, "output", "", "output .stratumarsenal path")
	fs.StringVar(&cfg.Output, "o", "", "output .stratumarsenal path")
	fs.StringVar(&cfg.Name, "name", "", "STRATUM template family name (default: OVF VM name)")
	fs.StringVar(&cfg.Version, "version", "1.0.0", "VM image version/tag")
	fs.StringVar(&cfg.Description, "description", "", "bundle/template description")
	fs.StringVar(&cfg.Icon, "icon", "dell.png", "STRATUM palette icon filename")
	fs.StringVar(&cfg.VMSelector, "vm", "", "VirtualSystem id or name when an OVF contains multiple VMs")
	fs.StringVar(&cfg.OVFDescriptor, "ovf-descriptor", "", "relative .ovf path when multiple descriptors exist")
	fs.StringVar(&cfg.Backend, "backend", "auto", "conversion backend: auto, qemu-img, or virt-v2v")
	fs.StringVar(&cfg.DiskBus, "disk-bus", "auto", "STRATUM disk policy: auto, preserve, scsi, or virtio")
	fs.StringVar(&cfg.NICModel, "nic-model", "auto", "portable STRATUM NIC model: auto, virtio, vmxnet3, or e1000e")
	fs.StringVar(&cfg.Firmware, "firmware", "auto", "firmware: auto, bios, uefi, or secureboot")
	fs.StringVar(&cfg.TPM, "tpm", "auto", "TPM policy: auto, none, or tpm2")
	fs.StringVar(&cfg.Arch, "arch", "auto", "guest architecture")
	fs.StringVar(&cfg.Identity, "identity", "preserve", "hardware identity: preserve or regenerate")
	fs.StringVar(&cfg.QEMUVersion, "qemu-version", "canvas", "STRATUM hypervisor engine: canvas, canvas3d, or stratum")
	fs.StringVar(&cfg.QEMUImg, "qemu-img", "", "path to qemu-img")
	fs.StringVar(&cfg.QCOW2Options, "qcow2-options", "compat=1.1,lazy_refcounts=on", "qemu-img qcow2 output options")
	fs.StringVar(&cfg.VirtV2V, "virt-v2v", "", "path to virt-v2v")
	fs.IntVar(&cfg.V2VParallel, "v2v-parallel", 2, "maximum parallel virt-v2v disk copies")
	fs.StringVar(&cfg.V2VRoot, "v2v-root", "first", "virt-v2v root selection: first, single, or ask")
	fs.StringVar(&cfg.V2VBlockDriver, "v2v-block-driver", "virtio-blk", "virt-v2v block driver: virtio-blk or virtio-scsi")
	fs.StringVar(&cfg.V2VTmpDir, "v2v-tmpdir", "", "large temporary storage for virt-v2v (VIRT_V2V_TMPDIR)")
	fs.Var(&v2vArgs, "virt-v2v-arg", "additional virt-v2v argument; repeat for each argument")
	fs.BoolVar(&cfg.PreserveVMwareNVRAM, "preserve-vmware-nvram", false, "retain source .nvram for audit only")
	fs.BoolVar(&cfg.PreserveV2VDiagnostics, "preserve-v2v-diagnostics", false, "include virt-v2v XML and log in migration-source/")
	fs.BoolVar(&cfg.SkipManifestCheck, "skip-manifest-check", false, "skip local OVF manifest verification")
	fs.BoolVar(&cfg.Overwrite, "overwrite", false, "replace an existing output file")
	fs.BoolVar(&cfg.KeepWorkdir, "keep-workdir", false, "retain temporary conversion directory")
	fs.BoolVar(&cfg.Quiet, "quiet", false, "suppress conversion progress; logs are still retained during execution")
	fs.StringVar(&cfg.Report, "report", "", "write a JSON migration report")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		printUsage(fs)
		return 2
	}
	cfg.Source = fs.Arg(0)
	cfg.V2VArgs = []string(v2vArgs)

	summary, err := migrate.Run(cfg, version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return 1
	}
	fmt.Printf("Created: %s\n", summary.Output)
	fmt.Printf("Template: %s\n", summary.TemplateName)
	fmt.Printf("VM image: %s\n", summary.ImageName)
	fmt.Printf("Backend: %s\n", summary.Result.Backend)
	fmt.Printf("Hardware: %d vCPU, %d MiB RAM, %d NIC(s)\n", summary.Model.CPU, summary.Model.RAMMiB, summary.Model.Ethernet)
	fmt.Printf("Firmware: %s; NIC: %s\n", summary.Firmware, summary.NICModel)
	for _, disk := range summary.Result.Disks {
		fmt.Printf("Disk: %s (%s) -> %s (%s, %s)\n",
			filepathBaseOrUnknown(disk.Source), disk.SourceBus, disk.OutputName, disk.OutputBus,
			migrate.FormatBytes(disk.OutputInfo.VirtualSize))
	}
	for _, warning := range summary.Model.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", warning)
	}
	if len(summary.Model.NVRAMFiles) > 0 && !cfg.PreserveVMwareNVRAM {
		fmt.Fprintln(os.Stderr, "WARNING: VMware .nvram was not included. STRATUM will create a fresh OVMF variable store on first boot.")
	}
	if summary.TPMEnabled && summary.Model.TPMPresent {
		fmt.Fprintln(os.Stderr, "WARNING: A fresh STRATUM TPM2 identity will be created; VMware vTPM secrets/state were not migrated.")
	}
	return 0
}

func filepathBaseOrUnknown(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "converted-disk"
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

func printUsage(fs *flag.FlagSet) {
	fmt.Fprintf(fs.Output(), `stratum-migrate %s

Convert a VMware OVA or unpacked OVF directory into a STRATUM Arsenal bundle.

Usage:
  stratum-migrate [options] appliance.ova
  stratum-migrate [options] /path/to/ovf-directory

Backends:
  auto       Prefer virt-v2v when installed; otherwise use qemu-img.
  virt-v2v   Inspect and modify Windows/Linux guests for KVM, install VirtIO
             support, and emit qcow2 disks before STRATUM packaging.
  qemu-img   Fast disk-format conversion without modifying the guest OS.

Examples:
  stratum-migrate vm.ova
  stratum-migrate --backend virt-v2v --report migration.json vm.ova
  stratum-migrate --backend qemu-img --disk-bus scsi ./exported-vm

Options:
`, version)
	fs.PrintDefaults()
	fmt.Fprintln(fs.Output(), "\nUse -V or --tool-version to print the tool version.")
}
