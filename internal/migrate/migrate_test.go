package migrate

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOVF(t *testing.T) {
	dir := t.TempDir()
	ovf := `<?xml version="1.0"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1" xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData">
  <References><File ovf:id="file1" ovf:href="disk.vmdk"/></References>
  <DiskSection><Disk ovf:diskId="disk1" ovf:fileRef="file1" ovf:capacity="10" ovf:capacityAllocationUnits="byte * 2^30"/></DiskSection>
  <VirtualSystem ovf:id="vm1">
    <Name>Finance VM</Name>
    <OperatingSystemSection ovf:id="103" ovf:version="Windows 11"><Description>Windows 11</Description></OperatingSystemSection>
    <VirtualHardwareSection>
      <Item><rasd:InstanceID>1</rasd:InstanceID><rasd:ResourceType>3</rasd:ResourceType><rasd:VirtualQuantity>4</rasd:VirtualQuantity></Item>
      <Item><rasd:InstanceID>2</rasd:InstanceID><rasd:ResourceType>4</rasd:ResourceType><rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits><rasd:VirtualQuantity>8192</rasd:VirtualQuantity></Item>
      <Item><rasd:InstanceID>3</rasd:InstanceID><rasd:ResourceType>6</rasd:ResourceType><rasd:ResourceSubType>lsilogicsas</rasd:ResourceSubType></Item>
      <Item><rasd:InstanceID>4</rasd:InstanceID><rasd:ResourceType>17</rasd:ResourceType><rasd:Parent>3</rasd:Parent><rasd:AddressOnParent>0</rasd:AddressOnParent><rasd:HostResource>ovf:/disk/disk1</rasd:HostResource></Item>
      <Item><rasd:InstanceID>5</rasd:InstanceID><rasd:ResourceType>10</rasd:ResourceType><rasd:ResourceSubType>VMXNET3</rasd:ResourceSubType></Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`
	if err := os.WriteFile(filepath.Join(dir, "guest.ovf"), []byte(ovf), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disk.vmdk"), []byte("disk"), 0o640); err != nil {
		t.Fatal(err)
	}
	model, err := ParseOVF(filepath.Join(dir, "guest.ovf"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if model.Name != "Finance VM" || model.CPU != 4 || model.RAMMiB != 8192 || model.GuestOS != "windows" {
		t.Fatalf("unexpected model: %+v", model)
	}
	if len(model.AttachedDisks) != 1 || model.AttachedDisks[0].SourceBus != "scsi" {
		t.Fatalf("unexpected disks: %+v", model.AttachedDisks)
	}
	if model.NICModel != "vmxnet3" {
		t.Fatalf("unexpected NIC model %q", model.NICModel)
	}
}

func TestSafeExtractOVARejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.ova")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := []byte("bad")
	if err := tw.WriteHeader(&tar.Header{Name: "../escape.ovf", Mode: 0o640, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SafeExtractOVA(archivePath, t.TempDir(), ExtractAll); err == nil || !strings.Contains(err.Error(), "unsafe OVA member") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
}

func TestDiskFilename(t *testing.T) {
	cases := map[string]string{"virtio": "virtioa.qcow2", "scsi": "sda.qcow2"}
	for bus, want := range cases {
		got, err := DiskFilename(bus, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: got %s want %s", bus, got, want)
		}
	}
}

func TestChooseOutputBusPortableContract(t *testing.T) {
	cases := []struct{ source, policy, backend, want string }{
		{"sata", "auto", "", "scsi"},
		{"ide", "preserve", "", "scsi"},
		{"scsi", "preserve", "", "scsi"},
		{"virtio", "preserve", "", "virtio"},
		{"sata", "auto", "virtio-blk", "virtio"},
	}
	for _, tc := range cases {
		if got := ChooseOutputBus(tc.source, tc.policy, tc.backend); got != tc.want {
			t.Fatalf("ChooseOutputBus(%q,%q,%q)=%q want %q", tc.source, tc.policy, tc.backend, got, tc.want)
		}
	}
}

func TestTemplateUsesPortableHardwareContract(t *testing.T) {
	y := RenderTemplateYAML(TemplateOptions{DisplayName: "x", Slug: "x", CPU: 1, RAMMiB: 512, Arch: "x86_64", Firmware: "uefi", NICModel: "vmxnet3", DiskBus: "scsi", QEMUVersion: "stratum", Icon: "dell.png"})
	for _, forbidden := range []string{"rtl8139", "e1000: e1000", "sata: sata", "ide: ide"} {
		if strings.Contains(y, forbidden) {
			t.Fatalf("template still exposes %q", forbidden)
		}
	}
	for _, required := range []string{"VMware VMXNET3", "Intel e1000e", "VirtIO Block", "VirtIO SCSI", `"stratum": "STRATUM"`} {
		if !strings.Contains(y, required) {
			t.Fatalf("template missing %q", required)
		}
	}
}
