# STRATUM Migrate

`stratum-migrate` converts a VMware-exported OVA file or unpacked OVF directory into a STRATUM Arsenal bundle (`.stratumarsenal`).

The `stratum-migrate` executable itself is a **fully static Go binary**. It does not require a Go runtime, libc, or other shared libraries on the migration host. External conversion tools are required only for the backend you choose.

## Conversion workflows

STRATUM Migrate supports two distinct conversion workflows:

| Workflow | Backend | Guest modification | Best use |
|---|---|---|---|
| **Enterprise / guest-aware migration** | `virt-v2v` | Yes | Production migrations, especially when Windows or Linux needs guest-side driver or boot adaptation |
| **Fast offline conversion** | `qemu-img` | No | Fast, simple disk conversion when the guest already supports the destination hardware or guest modification is not desired |

These are separate from the final STRATUM runtime hardware contract. A conversion backend can prepare or convert a guest without dictating which disk controller STRATUM exposes when the migrated VM boots.

### Enterprise / guest-aware migration

`virt-v2v` inspects Windows or Linux, performs guest-side conversion work for KVM, installs or enables available VirtIO support when appropriate, and writes QCOW2 disks.

Recommended invocation:

```bash
stratum-migrate \
  --backend virt-v2v \
  --report finance-server-migration.json \
  --preserve-v2v-diagnostics \
  finance-server.ova
```

The default backend is `auto`: use `virt-v2v` when it is installed, otherwise use `qemu-img`.

For a predictable enterprise workflow, explicitly select `--backend virt-v2v` so a missing or incompatible installation fails instead of silently selecting the fast path.

### Fast offline conversion

`qemu-img` performs direct disk-format conversion without mounting or modifying the guest OS. It does **not** require libguestfs and it does not install drivers inside the guest.

```bash
stratum-migrate \
  --backend qemu-img \
  --disk-bus scsi \
  --report finance-server-migration.json \
  finance-server.ova
```

This is the fastest and simplest migration path when the guest already has the drivers needed for its destination hardware, or when preserving the guest exactly as-is is more important than guest-aware conversion.

The qemu-img backend always normalizes the foreign disk image to QCOW2. For a supported UEFI guest targeting STRATUM, the default runtime disk interface is native **VMBus SCSI / StorVSC**. For a Legacy BIOS guest, the automatic engine policy selects **CANVAS**. Use `--backend virt-v2v` when guest-side driver, boot, or operating-system conversion work is required.

## Requirements

The `stratum-migrate` binary itself has no runtime library dependencies. Install only the external tools required by the selected conversion backend.

If you use only **Fast offline conversion**, `qemu-img` is the only external conversion tool required. You do **not** need `virt-v2v` or libguestfs for that path.

### Enterprise backend

Required:

```text
virt-v2v
libguestfs runtime/appliance support
qemu-img
```
To install virt-v2v, run the package manager install command for your specific Linux distribution: sudo dnf install virt-v2v (Fedora/RHEL/CentOS), sudo apt install virt-v2v (Ubuntu 22.04 and newer), or sudo apt-get install libguestfs-tools (older Ubuntu/Debian versions).
To install libguestfs, run the package manager install command for your specific Linux distribution: sudo dnf install libguestfs-tools (Fedora/RHEL/CentOS), sudo apt install libguestfs-tools (Debian/Ubuntu), or sudo zypper in guestfs-tools on openSUSE.
To install qemu-img, run the package manager install command for your specific Linux distribution: sudo apt install qemu-utils, etc...

Strongly recommended for Windows guests:

```text
virtio-win drivers, commonly under /usr/share/virtio-win
qemu-img for post-conversion qcow2 verification
```

Verify the host before a migration:

```bash
virt-v2v --version
virt-v2v --machine-readable | grep -E '^(input:ova|output:local|convert:windows|convert:linux)$'
qemu-img --version
```

`stratum-migrate` verifies that a detected `virt-v2v` advertises `input:ova` and `output:local` when machine-readable capabilities are available.

### Fast backend

Required:

```text
qemu-img
```

## Input

The source must be one of:

```text
/path/to/guest.ova
/path/to/unpacked-ovf-directory/
```

For `virt-v2v`, the appliance must be a VMware-exported OVA or VMware OVF folder. Virt-v2v's OVA support is VMware-specific.

When using a source OVA with the enterprise backend, STRATUM Migrate extracts only the OVF metadata needed to build the Arsenal template. Virt-v2v reads the original OVA, validates its manifest, inspects the guest, and performs the disk conversion. This avoids a second full VMDK extraction by the frontend.

When using an unpacked OVF directory, STRATUM Migrate verifies local `.mf` checksums before conversion unless `--skip-manifest-check` is supplied.

## Output

```text
finance-server-1.0.0.stratumarsenal
├── manifest.json
└── payload
    ├── templates
    │   └── finance-server
    │       └── canvas.yml
    └── vm-images
        └── finance-server-1.0.0
            ├── sda.qcow2
            ├── sdb.qcow2
            └── migration-source/        # optional audit material
```

## VM engine policy

The default is `--qemu-version auto`, which prefers STRATUM whenever the imported VM is compatible and safely falls back to CANVAS when it is not:

| Source VM | Default engine | Default runtime storage |
|---|---|---|
| UEFI x86_64 | **STRATUM** | **VMBus SCSI / StorVSC** |
| Secure Boot x86_64 | **STRATUM** | **VMBus SCSI / StorVSC** |
| UEFI aarch64 | **STRATUM** | **VMBus SCSI / StorVSC** |
| Legacy BIOS | **CANVAS** | CANVAS SCSI-compatible path |
| Unsupported STRATUM architecture/mode | **CANVAS** | CANVAS portable path |

STRATUM is never used automatically for a Legacy BIOS guest. An explicit `--qemu-version stratum` with Legacy BIOS fails with an actionable error instead of generating an unbootable bundle. The migration tool does not silently convert a BIOS-installed guest into a UEFI-installed guest.

The final runtime disk controller is selected separately from the conversion backend. For example, virt-v2v may use a VirtIO block driver while preparing a guest, while the finished UEFI Arsenal bundle still targets STRATUM with **VMBus SCSI / StorVSC**. Conversion-time hardware is not treated as the final runtime hardware contract.

## Backend behavior

### `--backend virt-v2v`

The generated command is equivalent to:

```bash
virt-v2v \
  -i ova SOURCE \
  -o local -os OUTPUT_DIRECTORY \
  -of qcow2 -oa sparse \
  -on TEMPLATE_NAME \
  --root first \
  --block-driver virtio-blk \
  --parallel 2
```

STRATUM Migrate then:

1. Reads the libvirt XML generated by virt-v2v.
2. Preserves the converted disk order from that XML.
3. Detects the converted guest firmware/NIC metadata while keeping conversion hardware separate from runtime hardware.
4. Applies the selected VM-engine runtime contract. STRATUM defaults fixed disks to VMBus SCSI / StorVSC and names them `sda.qcow2`, `sdb.qcow2`, and so on.
5. Runs `qemu-img check` and `qemu-img info` when qemu-img is available.
6. Builds and validates the `.stratumarsenal` package.

Useful options:

```bash
--v2v-parallel 4
--v2v-parallel 0     # omit --parallel for older virt-v2v releases
--v2v-root first
--v2v-root /dev/sda2
--v2v-block-driver virtio-blk
--v2v-block-driver virtio-scsi
--v2v-tmpdir /large-fast-volume/virt-v2v-temp
```

For multi-boot guests, use a specific root device after inspecting the appliance rather than relying on `first`.

Additional virt-v2v customization arguments can be passed without a shell:

```bash
stratum-migrate \
  --backend virt-v2v \
  --virt-v2v-arg=--no-fstrim \
  guest.ova
```

Repeat `--virt-v2v-arg` for every argument token. Managed input/output options cannot be overridden through this mechanism.

`--v2v-block-driver` controls virt-v2v's conversion-time guest preparation only. It does not force the final STRATUM runtime disk controller. Use `--disk-bus virtio` only when VirtIO Block is explicitly desired at runtime.

### `--backend qemu-img`

This backend converts each attached OVF disk to qcow2 using:

```text
compat=1.1,lazy_refcounts=on
```

It accepts `vmdk`, `vdi`, `vhdx`, `raw`, `qcow2`, `vpc`, and `qed` as **migration input formats** recognized by qemu-img, but every Arsenal VM disk is normalized to **QCOW2**. VMDK/VDI therefore remain import compatibility formats and are never emitted as STRATUM runtime disk formats. Gzip-compressed OVF disk references are decompressed before conversion.

Disk policies:

```text
auto       STRATUM: VMBus SCSI / StorVSC; CANVAS: use converted portable bus
preserve   Preserve portable VirtIO/SCSI; foreign SATA/IDE/LSI normalize to SCSI
scsi       Force SCSI (StorVSC under STRATUM, VirtIO SCSI under CANVAS)
virtio     Force VirtIO Block
```

## UEFI and VMware NVRAM

A VMware `.nvram` file is source hypervisor firmware state. Its presence alone does **not** prove that the VM uses UEFI, and it is never reused directly as STRATUM runtime firmware state.

STRATUM Migrate detects BIOS, UEFI, and Secure Boot from appliance metadata. Legacy BIOS automatically selects CANVAS under the default engine policy. For STRATUM UEFI guests, STRATUMVMM initializes fresh MSVM UEFI state in its VMGS store. CANVAS UEFI guests initialize their native UEFI variable store.

Consequences:

- VMware-specific EFI boot variables are not migrated.
- VMware custom Secure Boot variables/certificates are not migrated.
- A guest may need to rediscover its EFI bootloader.
- Keep the powered-off VMware source until the migrated VM has booted and been validated.

For audit retention only:

```bash
--preserve-vmware-nvram
```

The source file is stored under `migration-source/vmware-nvram/` and is never used as runtime firmware state.

## VMware vTPM, BitLocker, and sealed secrets

VMware vTPM state and secrets cannot be converted into a new STRATUM TPM identity. When the OVF declares a vTPM, the default `--tpm auto` enables a fresh STRATUM TPM 2.0 device and emits a warning.

Before migration:

- Suspend or decrypt TPM-sealed workloads where policy permits.
- Export BitLocker recovery keys.
- Confirm application-level keys and certificates are recoverable.
- Expect a recovery-key prompt on first boot for TPM-bound Windows volumes.

TPM policy overrides:

```bash
--tpm auto
--tpm none
--tpm tpm2
```

## Network identity

Virt-v2v preserves source MAC addresses in its generated metadata because guest network configuration can depend on them. A STRATUM Arsenal template/image bundle does not carry a deployed node's MAC identity, so STRATUM assigns MAC addresses when the node is deployed.

Plan for guest network-interface renaming, static-IP reassignment, firewall bindings, and software licensing tied to a source MAC address.

Virt-v2v does not generally move a static guest configuration to a different subnet automatically.

## Migration diagnostics

Create a JSON report:

```bash
--report migration.json
```

Include the virt-v2v log and generated libvirt XML inside the bundle for an auditable migration record:

```bash
--preserve-v2v-diagnostics
```

They are stored under:

```text
migration-source/virt-v2v/converted-domain.xml
migration-source/virt-v2v/virt-v2v.log
```

Review these files before distributing a bundle outside the organization; they can contain host paths, operating-system details, MAC addresses, and conversion diagnostics.

## Large migrations

Virt-v2v requires space for converted disks and potentially large temporary overlays. Use a dedicated fast volume:

```bash
mkdir -p /migration/virt-v2v-tmp
stratum-migrate \
  --backend virt-v2v \
  --v2v-tmpdir /migration/virt-v2v-tmp \
  --output /migration/bundles/finance-1.0.0.stratumarsenal \
  finance.ova
```

Also account for the final Arsenal ZIP package. The converter writes the package to `OUTPUT.partial` and atomically renames it after successful validation.

## Common examples

Enterprise Windows migration:

```bash
stratum-migrate \
  --backend virt-v2v \
  --name windows-finance \
  --version 1.0.0 \
  --v2v-block-driver virtio-blk \
  --v2v-parallel 4 \
  --v2v-tmpdir /migration/tmp \
  --report windows-finance.json \
  --preserve-v2v-diagnostics \
  windows-finance.ova
```

Enterprise Linux migration using virt-v2v's VirtIO SCSI conversion driver:

```bash
stratum-migrate \
  --backend virt-v2v \
  --v2v-block-driver virtio-scsi \
  --version 2.0.0 \
  linux-app.ova
```

Legacy BIOS guest (automatically targets CANVAS):

```bash
stratum-migrate \
  --backend qemu-img \
  --disk-bus scsi \
  legacy-appliance/
```

## Build

```bash
make test
make release VERSION=1.0.1
```

The release build uses:

```text
CGO_ENABLED=0
-trimpath
-s -w -buildid=
```

Binaries are generated for Linux amd64 and arm64.

Verify the amd64 binary is static:

```bash
file dist/stratum-migrate-linux-amd64
ldd dist/stratum-migrate-linux-amd64
```

`ldd` should report that it is not a dynamic executable.

## Security properties

- OVA extraction rejects absolute paths, path traversal, symlinks, hard links, devices, and FIFOs.
- OVF file references cannot escape the appliance directory.
- External commands are executed as argument arrays, not through a shell.
- Output is written to partial files and renamed only after success.
- Converted qcow2 disks are checked when qemu-img is available.
- The completed bundle is reopened and structurally validated.
- No VMware NVRAM or vTPM state is treated as native STRATUM state.

## Scope

Version 1.0.1 accepts exported OVA files and unpacked OVF directories. It does not connect directly to vCenter or ESXi. Direct VDDK, VMX-over-SSH, and vCenter sources can be added later without changing the STRATUM bundle stage.


## STRATUM portable output contract

`stratum-migrate` may ingest foreign hypervisor formats and hardware descriptions, but an Arsenal bundle is normalized before packaging:

- VM disks: **QCOW2**
- Disk interface: **VMBus SCSI / StorVSC** or **VirtIO Block** under STRATUM; **VirtIO SCSI** or **VirtIO Block** under CANVAS
- NIC: **VirtIO Network**, **e1000e**, or **VMXNET3**
- Default VMM engine: **STRATUM** for supported UEFI guests, **CANVAS** for Legacy BIOS/unsupported STRATUM firmware combinations
- VMM engine choices in the generated template: **STRATUM**, **CANVAS**, or **CANVAS 3D**

VMDK, VDI, VPC/QED, VMware SATA/IDE/LSI controllers, e1000, and rtl8139 are source compatibility concepts only. They are not emitted as STRATUM runtime hardware.
