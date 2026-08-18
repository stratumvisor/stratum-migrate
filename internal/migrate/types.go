package migrate

import "path/filepath"

const (
	BundleFormat  = "stratum-arsenal-bundle"
	BundleVersion = 1
)

type Config struct {
	Source                 string
	Output                 string
	Name                   string
	Version                string
	Description            string
	Icon                   string
	VMSelector             string
	OVFDescriptor          string
	Backend                string
	DiskBus                string
	NICModel               string
	Firmware               string
	TPM                    string
	Arch                   string
	Identity               string
	QEMUVersion            string
	QEMUImg                string
	QCOW2Options           string
	VirtV2V                string
	V2VParallel            int
	V2VRoot                string
	V2VBlockDriver         string
	V2VTmpDir              string
	V2VArgs                []string
	PreserveVMwareNVRAM    bool
	PreserveV2VDiagnostics bool
	SkipManifestCheck      bool
	Overwrite              bool
	KeepWorkdir            bool
	Quiet                  bool
	Report                 string
}

type FileReference struct {
	RefID       string
	Href        string
	Compression string
	Size        int64
	HasSize     bool
}

type DiskDefinition struct {
	DiskID        string
	FileRef       string
	Capacity      string
	CapacityUnits string
	ParentRef     string
}

type HardwareItem struct {
	InstanceID      string
	ResourceType    string
	ResourceSubtype string
	ElementName     string
	Description     string
	VirtualQuantity string
	AllocationUnits string
	Parent          string
	Address         string
	AddressOnParent string
	HostResource    string
	Connection      string
}

type AttachedDisk struct {
	DiskID          string
	Source          string
	SourceHref      string
	SourceBus       string
	ControllerID    string
	AddressOnParent int
	Compression     string
}

type OVFModel struct {
	Descriptor      string
	VirtualSystemID string
	Name            string
	Description     string
	CPU             int
	RAMMiB          int
	Ethernet        int
	GuestOS         string
	Arch            string
	Firmware        string
	SecureBoot      bool
	TPMPresent      bool
	NICModel        string
	HardwareUUID    string
	Files           map[string]FileReference
	Disks           map[string]DiskDefinition
	AttachedDisks   []AttachedDisk
	NVRAMFiles      []string
	MACAddresses    []string
	Warnings        []string
}

type DiskInfo struct {
	Format      string `json:"format,omitempty"`
	VirtualSize int64  `json:"virtualSizeBytes,omitempty"`
	ActualSize  int64  `json:"actualSizeBytes,omitempty"`
}

type ConvertedDisk struct {
	OVFDiskID  string
	Source     string
	SourceHref string
	SourceBus  string
	OutputBus  string
	OutputName string
	OutputPath string
	SourceInfo DiskInfo
	OutputInfo DiskInfo
}

type ConversionResult struct {
	Backend         string
	BackendVersion  string
	QEMUImgVersion  string
	Disks           []ConvertedDisk
	Firmware        string
	NICModel        string
	V2VLibvirtXML   string
	V2VLog          string
	V2VCapabilities []string
}

type BundleAsset struct {
	Root            string `json:"root"`
	Name            string `json:"name"`
	LogicalPath     string `json:"logicalPath"`
	ArchivePath     string `json:"archivePath"`
	ItemType        string `json:"itemType"`
	SHA256          string `json:"sha256"`
	SizeBytes       int64  `json:"sizeBytes"`
	Promoted        bool   `json:"promoted,omitempty"`
	TemplateName    string `json:"templateName,omitempty"`
	ParentImageName string `json:"parentImageName,omitempty"`
	SourceLabPath   string `json:"sourceLabPath,omitempty"`
	SourceNodeID    string `json:"sourceNodeId,omitempty"`
	SourceNodeName  string `json:"sourceNodeName,omitempty"`
	IncludesUEFI    bool   `json:"includesUefiState,omitempty"`
	IncludesTPM2    bool   `json:"includesTpm2State,omitempty"`
}

type BundleManifest struct {
	Format      string        `json:"format"`
	Version     int           `json:"version"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	CreatedAt   string        `json:"createdAt"`
	Template    *BundleAsset  `json:"template,omitempty"`
	VMImages    []BundleAsset `json:"vmImages,omitempty"`
	ISOs        []BundleAsset `json:"isos,omitempty"`
}

type MigrationReport struct {
	Tool                         string           `json:"tool"`
	ToolVersion                  string           `json:"toolVersion"`
	BundleFormat                 string           `json:"bundleFormat"`
	BundleVersion                int              `json:"bundleVersion"`
	Source                       string           `json:"source"`
	OVFDescriptor                string           `json:"ovfDescriptor"`
	VirtualSystemID              string           `json:"virtualSystemId"`
	VirtualSystemName            string           `json:"virtualSystemName"`
	Output                       string           `json:"output"`
	TemplateName                 string           `json:"templateName"`
	ImageName                    string           `json:"imageName"`
	Backend                      string           `json:"backend"`
	BackendVersion               string           `json:"backendVersion,omitempty"`
	QEMUImgVersion               string           `json:"qemuImgVersion,omitempty"`
	CPU                          int              `json:"cpu"`
	RAMMiB                       int              `json:"ramMiB"`
	Ethernet                     int              `json:"ethernet"`
	GuestOS                      string           `json:"guestOs"`
	Architecture                 string           `json:"architecture"`
	Firmware                     string           `json:"firmware"`
	NICModel                     string           `json:"nicModel"`
	TPMTemplateEnabled           bool             `json:"tpmTemplateEnabled"`
	IdentityPolicy               string           `json:"identityPolicy"`
	HardwareUUID                 string           `json:"hardwareUuid"`
	SourceMACAddresses           []string         `json:"sourceMacAddresses"`
	VMwareNVRAMDetected          []string         `json:"vmwareNvramDetected"`
	VMwareNVRAMPreservedForAudit bool             `json:"vmwareNvramPreservedForAudit"`
	V2VLibvirtXML                string           `json:"virtV2vLibvirtXml,omitempty"`
	V2VLog                       string           `json:"virtV2vLog,omitempty"`
	V2VCapabilities              []string         `json:"virtV2vCapabilities,omitempty"`
	Warnings                     []string         `json:"warnings"`
	Disks                        []map[string]any `json:"disks"`
}

func (d AttachedDisk) BaseName() string { return filepath.Base(d.Source) }
