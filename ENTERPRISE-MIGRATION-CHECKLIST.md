# STRATUM Enterprise Migration Checklist

## 1. Protect the source

- Shut down the VMware guest cleanly before export.
- Consolidate or remove snapshots before producing the OVA/OVF.
- Retain the powered-off VMware source until the STRATUM guest passes validation.
- Record the source VM name, UUID, CPU, RAM, disks, firmware, NICs, MAC addresses, and application owners.

## 2. Recoverability

- Export BitLocker recovery keys and any other TPM-sealed recovery material.
- Confirm application encryption keys, certificates, service-account credentials, and license recovery procedures.
- Record whether the source uses BIOS, UEFI, Secure Boot, or VMware vTPM.
- Plan for a new STRATUM TPM identity and a fresh OVMF variable store.

## 3. Migration host readiness

```bash
stratum-migrate -V
virt-v2v --version
virt-v2v --machine-readable | grep -E '^(input:ova|output:local|convert:windows|convert:linux)$'
qemu-img --version
```

For Windows, confirm VirtIO drivers are available through the host's virtio-win installation or `VIRTIO_WIN` configuration.

Confirm enough free space for:

- Virt-v2v temporary overlays.
- Converted qcow2 disks.
- The final `.stratumarsenal` package.

Use a dedicated fast volume with `--v2v-tmpdir` for large migrations.

## 4. Convert

Recommended enterprise command:

```bash
stratum-migrate \
  --backend virt-v2v \
  --name APPLICATION-NAME \
  --version 1.0.0 \
  --v2v-tmpdir /migration/tmp \
  --report APPLICATION-NAME-migration.json \
  --preserve-v2v-diagnostics \
  SOURCE.ova
```

Review every warning. Do not treat a successful conversion as proof that the guest will boot or that the application is healthy.

## 5. Review the migration record

- Confirm the selected backend is `virt-v2v`.
- Confirm every source disk has a corresponding STRATUM qcow2 disk.
- Confirm expected CPU, RAM, architecture, firmware, NIC model, and disk bus.
- Review `migration-source/virt-v2v/virt-v2v.log` when diagnostics were preserved.
- Review `migration-source/virt-v2v/converted-domain.xml` for disk order and target device models.
- Treat preserved diagnostics as potentially sensitive operational data.

## 6. Import and first boot

- Import the `.stratumarsenal` bundle into STRATUM Arsenal Forge.
- Deploy the guest into an isolated migration network first.
- Keep the original VMware VM powered off to prevent duplicate hostname, IP, UUID, or application identity conflicts.
- Have BitLocker and application recovery material available during first boot.
- Confirm the bootloader, filesystems, services, time synchronization, and guest tools.

## 7. Network validation

- Confirm interface names inside the guest.
- Reapply static IP, routes, DNS, MTU, VLAN, firewall, and proxy settings as needed.
- Check software licensing or application bindings tied to the VMware MAC address.
- Validate inbound and outbound application flows before production cutover.

## 8. Application validation

- Run application-specific smoke tests.
- Validate database consistency and transaction recovery.
- Validate storage latency and throughput.
- Validate backups, monitoring, logging, vulnerability scanning, and endpoint controls.
- Record acceptance by the application owner.

## 9. Cutover and rollback

- Define the final data synchronization or outage window.
- Record the rollback decision point.
- Do not delete the VMware source until the agreed rollback period has expired.
- Archive the migration report, bundle checksum, conversion log, approval, and validation results.
