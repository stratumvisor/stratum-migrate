package migrate

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type xmlElement struct {
	XMLName  xml.Name
	Attrs    []xml.Attr   `xml:",any,attr"`
	Text     string       `xml:",chardata"`
	Children []xmlElement `xml:",any"`
}

func (e *xmlElement) attrLocal(name string) string {
	for _, a := range e.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (e *xmlElement) directChild(name string) *xmlElement {
	for i := range e.Children {
		if e.Children[i].XMLName.Local == name {
			return &e.Children[i]
		}
	}
	return nil
}

func (e *xmlElement) directChildText(name string) string {
	if child := e.directChild(name); child != nil {
		return strings.TrimSpace(child.Text)
	}
	return ""
}

func (e *xmlElement) descendants(name string) []*xmlElement {
	var out []*xmlElement
	var walk func(*xmlElement)
	walk = func(curr *xmlElement) {
		if curr.XMLName.Local == name {
			out = append(out, curr)
		}
		for i := range curr.Children {
			walk(&curr.Children[i])
		}
	}
	walk(e)
	return out
}

func (e *xmlElement) firstDescendant(name string) *xmlElement {
	items := e.descendants(name)
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func FindOVFDescriptor(root, requested string) (string, error) {
	if requested != "" {
		candidate, err := SecureResolve(root, requested)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(candidate)
		if err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("requested OVF descriptor does not exist: %s", candidate)
		}
		return candidate, nil
	}
	paths, err := SortedWalkFiles(root, ".ovf")
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no .ovf descriptor found under %s", root)
	}
	if len(paths) > 1 {
		var rels []string
		for _, path := range paths {
			rel, _ := filepath.Rel(root, path)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return "", fmt.Errorf("multiple OVF descriptors were found; select one with --ovf-descriptor:\n  %s", strings.Join(rels, "\n  "))
	}
	return paths[0], nil
}

var manifestLinePattern = regexp.MustCompile(`(?i)^\s*(SHA1|SHA256|SHA512)\s*\((.+?)\)\s*=\s*([0-9a-f]+)\s*$`)

func VerifyOVFManifests(root string) error {
	paths, err := SortedWalkFiles(root, ".mf")
	if err != nil {
		return err
	}
	for _, manifestPath := range paths {
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		text := strings.TrimPrefix(string(body), "\ufeff")
		for lineNo, line := range strings.Split(text, "\n") {
			line = strings.TrimSuffix(line, "\r")
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			match := manifestLinePattern.FindStringSubmatch(line)
			if match == nil {
				return fmt.Errorf("unsupported manifest line in %s:%d: %s", filepath.Base(manifestPath), lineNo+1, trimmed)
			}
			target, err := SecureResolve(filepath.Dir(manifestPath), match[2])
			if err != nil {
				return err
			}
			st, err := os.Stat(target)
			if err != nil || !st.Mode().IsRegular() {
				return fmt.Errorf("manifest references a missing file: %s:%d: %s", filepath.Base(manifestPath), lineNo+1, match[2])
			}
			actual, err := HashFile(target, strings.ToUpper(match[1]))
			if err != nil {
				return err
			}
			if !strings.EqualFold(actual, match[3]) {
				return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", match[2], strings.ToLower(match[3]), strings.ToLower(actual))
			}
		}
	}
	return nil
}

func ParseOVF(descriptor, vmSelector string, requireDiskFiles bool) (*OVFModel, error) {
	body, err := os.ReadFile(descriptor)
	if err != nil {
		return nil, err
	}
	var root xmlElement
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("invalid OVF XML: %w", err)
	}
	virtualSystems := root.descendants("VirtualSystem")
	if len(virtualSystems) == 0 {
		return nil, fmt.Errorf("OVF contains no VirtualSystem")
	}
	var selected *xmlElement
	var choices []string
	for _, vs := range virtualSystems {
		id := vs.attrLocal("id")
		name := vs.directChildText("Name")
		if name == "" {
			name = id
		}
		choices = append(choices, fmt.Sprintf("%s: %s", valueOr(id, "<no-id>"), name))
		if vmSelector != "" && (vmSelector == id || vmSelector == name) {
			selected = vs
		}
	}
	if vmSelector != "" && selected == nil {
		return nil, fmt.Errorf("VirtualSystem %q was not found. Available systems:\n  %s", vmSelector, strings.Join(choices, "\n  "))
	}
	if selected == nil {
		if len(virtualSystems) > 1 {
			return nil, fmt.Errorf("OVF contains multiple VirtualSystem entries; select one with --vm:\n  %s", strings.Join(choices, "\n  "))
		}
		selected = virtualSystems[0]
	}

	vsID := selected.attrLocal("id")
	name := selected.directChildText("Name")
	if name == "" {
		name = vsID
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(descriptor), filepath.Ext(descriptor))
	}
	description := ""
	if annotation := selected.firstDescendant("AnnotationSection"); annotation != nil {
		description = annotation.directChildText("Annotation")
	}
	if description == "" {
		description = fmt.Sprintf("Imported from OVF appliance %s", filepath.Base(descriptor))
	}

	files := map[string]FileReference{}
	for _, el := range root.descendants("File") {
		id, href := el.attrLocal("id"), el.attrLocal("href")
		if id == "" || href == "" {
			continue
		}
		ref := FileReference{RefID: id, Href: href, Compression: el.attrLocal("compression")}
		if sizeText := el.attrLocal("size"); sizeText != "" {
			if size, err := strconv.ParseInt(sizeText, 10, 64); err == nil {
				ref.Size, ref.HasSize = size, true
			}
		}
		files[id] = ref
	}

	disks := map[string]DiskDefinition{}
	var diskOrder []string
	for _, el := range root.descendants("Disk") {
		id := el.attrLocal("diskId")
		if id == "" {
			continue
		}
		disks[id] = DiskDefinition{
			DiskID: id, FileRef: el.attrLocal("fileRef"), Capacity: el.attrLocal("capacity"),
			CapacityUnits: el.attrLocal("capacityAllocationUnits"), ParentRef: el.attrLocal("parentRef"),
		}
		diskOrder = append(diskOrder, id)
	}

	hardwareSection := selected.firstDescendant("VirtualHardwareSection")
	if hardwareSection == nil {
		return nil, fmt.Errorf("selected VirtualSystem has no VirtualHardwareSection")
	}
	var items []HardwareItem
	for _, item := range hardwareSection.descendants("Item") {
		items = append(items, parseHardwareItem(item))
	}
	cpu, ramMiB := 0, 0
	for _, item := range items {
		switch item.ResourceType {
		case "3":
			cpu += maxInt(0, parseInt(item.VirtualQuantity, 0))
		case "4":
			if bytes, ok := parseScaledBytes(item.VirtualQuantity, item.AllocationUnits); ok {
				ramMiB += maxInt(1, int(math.Ceil(float64(bytes)/float64(1024*1024))))
			} else {
				ramMiB += maxInt(0, parseInt(item.VirtualQuantity, 0))
			}
		}
	}
	if cpu == 0 {
		cpu = 2
	}
	if ramMiB == 0 {
		ramMiB = 2048
	}
	ethernet := 0
	for _, item := range items {
		if item.ResourceType == "10" {
			ethernet++
		}
	}

	descriptorDir := filepath.Dir(descriptor)
	var nvramFiles []string
	nvramSeen := map[string]bool{}
	for _, ref := range files {
		if strings.EqualFold(filepath.Ext(ref.Href), ".nvram") {
			path, err := SecureResolve(descriptorDir, ref.Href)
			if err == nil {
				nvramSeen[path] = true
				if st, statErr := os.Stat(path); statErr == nil && st.Mode().IsRegular() {
					nvramFiles = append(nvramFiles, path)
				}
			}
		}
	}
	if discovered, err := SortedWalkFiles(descriptorDir, ".nvram"); err == nil {
		for _, path := range discovered {
			if !nvramSeen[path] {
				nvramFiles = append(nvramFiles, path)
				nvramSeen[path] = true
			}
		}
	}

	firmware, secureBoot, tpmPresent, config := parseFirmwareAndTPM(selected, len(nvramSeen) > 0)
	guestOS, arch := detectGuestOS(selected)
	nicModel := pickNICModel(items)
	macs := collectMACs(items)
	uuid := detectUUID(config)

	controllers := map[string]string{}
	for _, item := range items {
		bus := controllerBus(item)
		if item.InstanceID != "" && bus != "unknown" {
			controllers[item.InstanceID] = bus
		}
	}
	type attachedRef struct {
		parent, address, ordinal        int
		diskID, sourceBus, controllerID string
	}
	var refs []attachedRef
	for ordinal, item := range items {
		if item.ResourceType != "17" {
			continue
		}
		diskID := extractDiskID(item.HostResource)
		if diskID == "" {
			continue
		}
		refs = append(refs, attachedRef{
			parent: parseInt(item.Parent, 10000), address: parseInt(valueOr(item.AddressOnParent, item.Address), ordinal),
			ordinal: ordinal, diskID: diskID, sourceBus: valueOr(controllers[item.Parent], "unknown"), controllerID: item.Parent,
		})
	}
	if len(refs) == 0 {
		for ordinal, id := range diskOrder {
			refs = append(refs, attachedRef{parent: 0, address: ordinal, ordinal: ordinal, diskID: id, sourceBus: "unknown"})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].parent != refs[j].parent {
			return refs[i].parent < refs[j].parent
		}
		if refs[i].address != refs[j].address {
			return refs[i].address < refs[j].address
		}
		return NaturalLess(refs[i].diskID, refs[j].diskID)
	})

	var attached []AttachedDisk
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.diskID] {
			continue
		}
		seen[ref.diskID] = true
		def, ok := disks[ref.diskID]
		if !ok {
			return nil, fmt.Errorf("hardware references undefined OVF disk: %s", ref.diskID)
		}
		fileRef, ok := files[def.FileRef]
		if !ok {
			return nil, fmt.Errorf("OVF disk %s references missing File id %q", ref.diskID, def.FileRef)
		}
		sourcePath, err := SecureResolve(descriptorDir, fileRef.Href)
		if err != nil {
			return nil, err
		}
		if requireDiskFiles {
			if st, err := os.Stat(sourcePath); err != nil || !st.Mode().IsRegular() {
				return nil, fmt.Errorf("OVF disk file does not exist: %s", fileRef.Href)
			}
		}
		attached = append(attached, AttachedDisk{
			DiskID: ref.diskID, Source: sourcePath, SourceHref: fileRef.Href, SourceBus: ref.sourceBus,
			ControllerID: ref.controllerID, AddressOnParent: ref.address, Compression: fileRef.Compression,
		})
	}
	if len(attached) == 0 {
		return nil, fmt.Errorf("OVF contains no attached virtual disks")
	}

	var warnings []string
	if len(nvramSeen) > 0 {
		warnings = append(warnings, "VMware NVRAM was detected. It is not OVMF-compatible and will not be used as STRATUM uefi-vars.fd.")
	}
	if tpmPresent {
		warnings = append(warnings, "A VMware virtual TPM was detected. VMware vTPM state cannot be converted; the guest may require BitLocker or recovery-key handling.")
	}
	if len(macs) > 0 {
		warnings = append(warnings, "Source NIC MAC address(es) were detected, but an Arsenal template/image bundle does not carry deployed-node MAC identity; STRATUM will assign MAC addresses when a node is deployed.")
	}

	return &OVFModel{
		Descriptor: descriptor, VirtualSystemID: vsID, Name: name, Description: description,
		CPU: cpu, RAMMiB: ramMiB, Ethernet: ethernet, GuestOS: guestOS, Arch: arch,
		Firmware: firmware, SecureBoot: secureBoot, TPMPresent: tpmPresent, NICModel: nicModel,
		HardwareUUID: uuid, Files: files, Disks: disks, AttachedDisks: attached,
		NVRAMFiles: nvramFiles, MACAddresses: macs, Warnings: warnings,
	}, nil
}

func parseHardwareItem(item *xmlElement) HardwareItem {
	return HardwareItem{
		InstanceID: item.directChildText("InstanceID"), ResourceType: item.directChildText("ResourceType"),
		ResourceSubtype: item.directChildText("ResourceSubType"), ElementName: item.directChildText("ElementName"),
		Description: item.directChildText("Description"), VirtualQuantity: item.directChildText("VirtualQuantity"),
		AllocationUnits: item.directChildText("AllocationUnits"), Parent: item.directChildText("Parent"),
		Address: item.directChildText("Address"), AddressOnParent: item.directChildText("AddressOnParent"),
		HostResource: item.directChildText("HostResource"), Connection: item.directChildText("Connection"),
	}
}

func controllerBus(item HardwareItem) string {
	text := strings.ToLower(strings.Join([]string{item.ResourceSubtype, item.ElementName, item.Description}, " "))
	switch {
	case strings.Contains(text, "nvme"):
		return "nvme"
	case strings.Contains(text, "sata") || strings.Contains(text, "ahci"):
		return "sata"
	case strings.Contains(text, "ide") || item.ResourceType == "5":
		return "ide"
	case strings.Contains(text, "scsi") || strings.Contains(text, "lsilogic") || strings.Contains(text, "buslogic") || strings.Contains(text, "pvscsi") || item.ResourceType == "6":
		return "scsi"
	case item.ResourceType == "20":
		return "sata"
	default:
		return "unknown"
	}
}

var diskIDPattern = regexp.MustCompile(`(?i)(?:^|/)disk/([^/]+)$`)

func extractDiskID(value string) string {
	decoded, _ := urlPathUnescape(strings.TrimSpace(value))
	if match := diskIDPattern.FindStringSubmatch(decoded); match != nil {
		return match[1]
	}
	if strings.HasPrefix(strings.ToLower(decoded), "ovf:/disk/") {
		parts := strings.Split(decoded, "/")
		return parts[len(parts)-1]
	}
	return ""
}

func urlPathUnescape(value string) (string, error) {
	// net/url.QueryUnescape incorrectly treats '+' as a space. OVF hrefs and
	// host resources use percent-encoding, so use PathUnescape through helper.
	return pathUnescape(value)
}

func detectGuestOS(vs *xmlElement) (string, string) {
	var values []string
	if section := vs.firstDescendant("OperatingSystemSection"); section != nil {
		for _, attr := range section.Attrs {
			values = append(values, attr.Value)
		}
		var collect func(*xmlElement)
		collect = func(e *xmlElement) {
			values = append(values, e.Text)
			for i := range e.Children {
				collect(&e.Children[i])
			}
		}
		collect(section)
	}
	for _, cfg := range vs.descendants("Config") {
		key := strings.ToLower(cfg.attrLocal("key"))
		if strings.Contains(key, "guestos") || key == "guestid" {
			values = append(values, cfg.attrLocal("value"))
		}
	}
	joined := strings.ToLower(strings.Join(values, " "))
	guestOS := "other"
	for _, token := range []string{"windows", "win11", "win10", "win8", "win7", "winvista", "winxp", "winserver", "winnet"} {
		if strings.Contains(joined, token) {
			guestOS = "windows"
			break
		}
	}
	if guestOS == "other" {
		for _, token := range []string{"linux", "ubuntu", "debian", "centos", "rhel", "redhat", "suse", "sles", "oraclelinux", "photon", "coreos", "fedora", "rocky", "alma", "archlinux"} {
			if strings.Contains(joined, token) {
				guestOS = "linux"
				break
			}
		}
	}
	arch := "x86_64"
	if strings.Contains(joined, "aarch64") || strings.Contains(joined, "arm64") || strings.Contains(joined, "arm-64") {
		arch = "aarch64"
	} else if strings.Contains(joined, "32") && !strings.Contains(joined, "64") {
		arch = "i386"
	}
	return guestOS, arch
}

func pickNICModel(items []HardwareItem) string {
	counts := map[string]int{}
	order := []string{"vmxnet3", "e1000e", "e1000", "rtl8139", "virtio"}
	for _, item := range items {
		if item.ResourceType != "10" {
			continue
		}
		text := strings.ToLower(strings.Join([]string{item.ResourceSubtype, item.ElementName, item.Description}, " "))
		for _, model := range order {
			if strings.Contains(text, model) {
				counts[model]++
				break
			}
		}
	}
	best, bestCount := "e1000e", 0
	for _, model := range order {
		if counts[model] > bestCount {
			best, bestCount = model, counts[model]
		}
	}
	return best
}

func parseFirmwareAndTPM(vs *xmlElement, nvramPresent bool) (string, bool, bool, map[string]string) {
	config := map[string]string{}
	for _, cfg := range vs.descendants("Config") {
		key := strings.ToLower(strings.TrimSpace(cfg.attrLocal("key")))
		if key != "" {
			config[key] = strings.TrimSpace(cfg.attrLocal("value"))
		}
	}
	firmware := "bios"
	var fwValues []string
	for key, value := range config {
		if key == "firmware" || key == "firmwaretype" || key == "efi" || strings.Contains(key, "firmware") {
			fwValues = append(fwValues, value)
		}
	}
	joined := strings.ToLower(strings.Join(fwValues, " "))
	if strings.Contains(joined, "efi") || strings.Contains(joined, "uefi") || nvramPresent {
		firmware = "uefi"
	}
	secureBoot := false
	for key, value := range config {
		normalized := strings.NewReplacer(".", "", "_", "").Replace(key)
		if (strings.Contains(normalized, "secureboot") || strings.Contains(key, "secure.boot")) && boolFromText(value) {
			secureBoot = true
			firmware = "secureboot"
		}
	}
	tpm := false
	for key, value := range config {
		if strings.Contains(key, "tpm") && boolFromText(value) {
			tpm = true
			break
		}
	}
	if !tpm {
		for _, item := range vs.descendants("Item") {
			hw := parseHardwareItem(item)
			text := strings.ToLower(strings.Join([]string{hw.ResourceSubtype, hw.ElementName, hw.Description}, " "))
			if strings.Contains(text, "tpm") {
				tpm = true
				break
			}
		}
	}
	return firmware, secureBoot, tpm, config
}

func collectMACs(items []HardwareItem) []string {
	seen := map[string]bool{}
	var out []string
	nonHex := regexp.MustCompile(`[^0-9A-Fa-f]`)
	for _, item := range items {
		if item.ResourceType != "10" {
			continue
		}
		candidate := nonHex.ReplaceAllString(item.Address, "")
		if len(candidate) != 12 {
			continue
		}
		parts := make([]string, 0, 6)
		for i := 0; i < 12; i += 2 {
			parts = append(parts, strings.ToLower(candidate[i:i+2]))
		}
		mac := strings.Join(parts, ":")
		if !seen[mac] {
			seen[mac] = true
			out = append(out, mac)
		}
	}
	return out
}

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func detectUUID(config map[string]string) string {
	for _, key := range []string{"uuid.bios", "uuid", "smbios.reflecthost"} {
		candidate := strings.Trim(strings.TrimSpace(config[key]), `"`)
		if uuidPattern.MatchString(candidate) {
			return strings.ToLower(candidate)
		}
	}
	return ""
}

func parseScaledBytes(value, units string) (int64, bool) {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(units), ""))
	multiplier := float64(1)
	power2 := regexp.MustCompile(`2\^(\d+)`).FindStringSubmatch(normalized)
	power10 := regexp.MustCompile(`10\^(\d+)`).FindStringSubmatch(normalized)
	switch {
	case power2 != nil:
		p, _ := strconv.Atoi(power2[1])
		multiplier = math.Pow(2, float64(p))
	case power10 != nil:
		p, _ := strconv.Atoi(power10[1])
		multiplier = math.Pow(10, float64(p))
	case strings.Contains(normalized, "gib") || strings.Contains(normalized, "gigabyte") || strings.Contains(normalized, "gb"):
		multiplier = 1024 * 1024 * 1024
	case strings.Contains(normalized, "mib") || strings.Contains(normalized, "megabyte") || strings.Contains(normalized, "mb"):
		multiplier = 1024 * 1024
	case strings.Contains(normalized, "kib") || strings.Contains(normalized, "kilobyte") || strings.Contains(normalized, "kb"):
		multiplier = 1024
	}
	return int64(number * multiplier), true
}

func parseInt(value string, fallback int) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	return int(f)
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
func boolFromText(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled", "enable":
		return true
	default:
		return false
	}
}
