package ovirt

import (
	"context"
	"fmt"
	liberr "github.com/konveyor/controller/pkg/error"
	libitr "github.com/konveyor/controller/pkg/itinerary"
	api "github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/plan"
	"github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1/ref"
	plancontext "github.com/konveyor/forklift-controller/pkg/controller/plan/context"
	"github.com/konveyor/forklift-controller/pkg/controller/provider/web/ocp"
	model "github.com/konveyor/forklift-controller/pkg/controller/provider/web/ovirt"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	cnv "kubevirt.io/client-go/api/v1"
	cdi "kubevirt.io/containerized-data-importer/pkg/apis/core/v1beta1"
	"path"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

// BIOS types
const (
	ClusterDefault = "cluster_default"
	Q35Ovmf        = "q35_ovmf"
)

// Bus types
const (
	VirtioScsi = "virtio_scsi"
	Virtio     = "virtio"
	Sata       = "sata"
	Scsi       = "scsi"
)

// Input types
const (
	Tablet = "tablet"
)

// Network types
const (
	Pod    = "pod"
	Multus = "multus"
)

// Template labels
const (
	TemplateOSLabel       = "os.template.kubevirt.io/%s"
	TemplateWorkloadLabel = "workload.template.kubevirt.io/server"
	TemplateFlavorLabel   = "flavor.template.kubevirt.io/medium"
)

// Operating Systems
const (
	DefaultWindows = "win10"
	DefaultLinux   = "rhel8.1"
	Unknown        = "unknown"
)

// Map of ovirt guest ids to osinfo ids.
var osMap = map[string]string{
	"rhel_6_10_plus_ppc64": "rhel6.10",
	"rhel_6_ppc64":         "rhel6.10",
	"rhel_6":               "rhel6.10",
	"rhel_6x64":            "rhel6.10",
	"rhel_6_9_plus_ppc64":  "rhel6.9",
	"rhel_7_ppc64":         "rhel7.7",
	"rhel_7_s390x":         "rhel7.7",
	"rhel_7x64":            "rhel7.7",
	"rhel_8x64":            "rhel8.1",
	"sles_11_ppc64":        "opensuse15.0",
	"sles_11":              "opensuse15.0",
	"sles_12_s390x":        "opensuse15.0",
	"ubuntu_12_04":         "ubuntu18.04",
	"ubuntu_12_10":         "ubuntu18.04",
	"ubuntu_13_04":         "ubuntu18.04",
	"ubuntu_13_10":         "ubuntu18.04",
	"ubuntu_14_04_ppc64":   "ubuntu18.04",
	"ubuntu_14_04":         "ubuntu18.04",
	"ubuntu_16_04_s390x":   "ubuntu18.04",
	"windows_10":           "win10",
	"windows_10x64":        "win10",
	"windows_2003":         "win10",
	"windows_2003x64":      "win10",
	"windows_2008R2x64":    "win2k8",
	"windows_2008":         "win2k8",
	"windows_2008x64":      "win2k8",
	"windows_2012R2x64":    "win2k12r2",
	"windows_2012x64":      "win2k12r2",
	"windows_2016x64":      "win2k16",
	"windows_2019x64":      "win2k19",
	"windows_7":            "win10",
	"windows_7x64":         "win10",
	"windows_8":            "win10",
	"windows_8x64":         "win10",
	"windows_xp":           "win10",
}

//
// oVirt builder.
type Builder struct {
	*plancontext.Context
	// Provisioner CRs.
	provisioners map[string]*api.Provisioner
}

//
// Create DataVolume certificate configmap.
func (r *Builder) ConfigMap(_ ref.Ref, in *core.Secret, object *core.ConfigMap) (err error) {
	object.BinaryData["ca.pem"] = in.Data["cacert"]
	return
}

//
// Build the DataVolume credential secret.
func (r *Builder) Secret(_ ref.Ref, in, object *core.Secret) (err error) {
	object.StringData = map[string]string{
		"accessKeyId": string(in.Data["user"]),
		"secretKey":   string(in.Data["password"]),
	}
	return
}

//
// Create DataVolume specs for the VM.
func (r *Builder) DataVolumes(vmRef ref.Ref, secret *core.Secret, configMap *core.ConfigMap) (dvs []cdi.DataVolumeSpec, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}
	url := r.Source.Provider.Spec.URL

	dsMapIn := r.Context.Map.Storage.Spec.Map
	for i := range dsMapIn {
		mapped := &dsMapIn[i]
		ref := mapped.Source
		sd := &model.StorageDomain{}
		fErr := r.Source.Inventory.Find(sd, ref)
		if fErr != nil {
			err = fErr
			return
		}
		mErr := r.defaultModes(&mapped.Destination)
		if mErr != nil {
			err = mErr
			return
		}
		for _, da := range vm.DiskAttachments {
			if da.Disk.StorageDomain == sd.ID {
				storageClass := mapped.Destination.StorageClass
				volumeMode := core.PersistentVolumeFilesystem
				if mapped.Destination.VolumeMode != "" {
					volumeMode = mapped.Destination.VolumeMode
				}
				accessMode := core.ReadWriteOnce
				if mapped.Destination.AccessMode != "" {
					accessMode = mapped.Destination.AccessMode
				}
				dvSpec := cdi.DataVolumeSpec{
					Source: cdi.DataVolumeSource{
						Imageio: &cdi.DataVolumeSourceImageIO{
							URL:           url,
							DiskID:        da.Disk.ID,
							SecretRef:     secret.Name,
							CertConfigMap: configMap.Name,
						},
					},
					Storage: &cdi.StorageSpec{
						AccessModes: []core.PersistentVolumeAccessMode{
							accessMode,
						},
						VolumeMode: &volumeMode,
						Resources: core.ResourceRequirements{
							Requests: core.ResourceList{
								core.ResourceStorage: *resource.NewQuantity(da.Disk.ProvisionedSize, resource.BinarySI),
							},
						},
						StorageClassName: &storageClass,
					},
				}
				dvs = append(dvs, dvSpec)
			}
		}
	}

	return
}

//
// Create the destination Kubevirt VM.
func (r *Builder) VirtualMachine(vmRef ref.Ref, object *cnv.VirtualMachineSpec, dataVolumes []cdi.DataVolume) (err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}
	object.Template = &cnv.VirtualMachineInstanceTemplateSpec{}
	r.mapDisks(vm, dataVolumes, object)
	r.mapFirmware(vm, &vm.Cluster, object)
	r.mapCPU(vm, object)
	r.mapMemory(vm, object)
	r.mapClock(vm, object)
	r.mapInput(object)
	err = r.mapNetworks(vm, object)
	if err != nil {
		return
	}

	return
}

func (r *Builder) mapNetworks(vm *model.Workload, object *cnv.VirtualMachineSpec) (err error) {
	var kNetworks []cnv.Network
	var kInterfaces []cnv.Interface

	netMapIn := r.Context.Map.Network.Spec.Map
	for i := range netMapIn {
		mapped := &netMapIn[i]
		ref := mapped.Source
		network := &model.Network{}
		fErr := r.Source.Inventory.Find(network, ref)
		if fErr != nil {
			err = fErr
			return
		}
		needed := false
		passThrough := false
		mac := ""
		for _, nic := range vm.NICs {
			if nic.Profile.Network == network.ID {
				needed = true
				passThrough = nic.Profile.PassThrough
				mac = nic.Mac
				break
			}
		}
		if !needed {
			continue
		}
		networkName := fmt.Sprintf("net-%v", i)
		kNetwork := cnv.Network{
			Name: networkName,
		}
		kInterface := cnv.Interface{
			Name:       networkName,
			Model:      Virtio,
			MacAddress: mac,
		}
		switch mapped.Destination.Type {
		case Pod:
			kNetwork.Pod = &cnv.PodNetwork{}
			kInterface.Masquerade = &cnv.InterfaceMasquerade{}
		case Multus:
			kNetwork.Multus = &cnv.MultusNetwork{
				NetworkName: path.Join(mapped.Destination.Namespace, mapped.Destination.Name),
			}
			if passThrough {
				kInterface.SRIOV = &cnv.InterfaceSRIOV{}
			} else {
				kInterface.Bridge = &cnv.InterfaceBridge{}
			}
		}
		kNetworks = append(kNetworks, kNetwork)
		kInterfaces = append(kInterfaces, kInterface)
	}
	object.Template.Spec.Networks = kNetworks
	object.Template.Spec.Domain.Devices.Interfaces = kInterfaces
	return
}

func (r *Builder) mapInput(object *cnv.VirtualMachineSpec) {
	tablet := cnv.Input{
		Type: Tablet,
		Name: Tablet,
		Bus:  Virtio,
	}
	object.Template.Spec.Domain.Devices.Inputs = []cnv.Input{tablet}
}

func (r *Builder) mapClock(vm *model.Workload, object *cnv.VirtualMachineSpec) {
	clock := cnv.Clock{
		Timer: &cnv.Timer{},
	}
	timezone := cnv.ClockOffsetTimezone(vm.Timezone)
	clock.Timezone = &timezone
	object.Template.Spec.Domain.Clock = &clock
}

func (r *Builder) mapMemory(vm *model.Workload, object *cnv.VirtualMachineSpec) {
	reservation := resource.NewQuantity(vm.Memory, resource.BinarySI)
	object.Template.Spec.Domain.Resources = cnv.ResourceRequirements{
		Requests: map[core.ResourceName]resource.Quantity{
			core.ResourceMemory: *reservation,
		},
	}
}
func (r *Builder) mapCPU(vm *model.Workload, object *cnv.VirtualMachineSpec) {
	object.Template.Spec.Domain.Machine = &cnv.Machine{Type: "q35"}
	object.Template.Spec.Domain.CPU = &cnv.CPU{
		Sockets: uint32(vm.CpuSockets),
		Cores:   uint32(vm.CpuCores),
		Threads: uint32(vm.CpuThreads),
	}
}

func (r *Builder) mapFirmware(vm *model.Workload, cluster *model.Cluster, object *cnv.VirtualMachineSpec) {
	biosType := vm.BIOS
	if biosType == ClusterDefault {
		biosType = cluster.BiosType
	}
	serial := vm.SerialNumber
	if serial == "" {
		serial = vm.ID
	}
	features := &cnv.Features{}
	firmware := &cnv.Firmware{
		Serial: serial,
	}
	switch biosType {
	case Q35Ovmf:
		smmEnabled := true
		features.SMM = &cnv.FeatureState{
			Enabled: &smmEnabled,
		}
		firmware.Bootloader = &cnv.Bootloader{EFI: &cnv.EFI{}}
	default:
		firmware.Bootloader = &cnv.Bootloader{BIOS: &cnv.BIOS{}}
	}
	object.Template.Spec.Domain.Features = features
	object.Template.Spec.Domain.Firmware = firmware
}

func (r *Builder) mapDisks(vm *model.Workload, dataVolumes []cdi.DataVolume, object *cnv.VirtualMachineSpec) {
	dvMap := make(map[string]*cdi.DataVolume)
	for i := range dataVolumes {
		dv := &dataVolumes[i]
		dvMap[dv.Spec.Source.Imageio.DiskID] = dv
	}

	for i, da := range vm.DiskAttachments {
		dv := dvMap[da.Disk.ID]
		volumeName := fmt.Sprintf("vol-%v", i)
		volume := cnv.Volume{
			Name: volumeName,
			VolumeSource: cnv.VolumeSource{
				DataVolume: &cnv.DataVolumeSource{
					Name: dv.Name,
				},
			},
		}
		var bus string
		switch da.Interface {
		case VirtioScsi:
			bus = Scsi
		case Sata:
			bus = Sata
		default:
			bus = Virtio
		}
		disk := cnv.Disk{
			Name: volumeName,
			DiskDevice: cnv.DiskDevice{
				Disk: &cnv.DiskTarget{
					Bus: bus,
				},
			},
		}
		object.Template.Spec.Volumes = append(object.Template.Spec.Volumes, volume)
		object.Template.Spec.Domain.Devices.Disks = append(object.Template.Spec.Domain.Devices.Disks, disk)
	}
}

//
// Set volume and access modes.
func (r *Builder) defaultModes(dm *api.DestinationStorage) (err error) {
	model := &ocp.StorageClass{}
	ref := ref.Ref{Name: dm.StorageClass}
	err = r.Destination.Inventory.Find(model, ref)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	if dm.VolumeMode == "" || dm.AccessMode == "" {
		if provisioner, found := r.provisioners[model.Object.Provisioner]; found {
			volumeMode := provisioner.VolumeMode(dm.VolumeMode)
			accessMode := volumeMode.AccessMode(dm.AccessMode)
			if dm.VolumeMode == "" {
				dm.VolumeMode = volumeMode.Name
			}
			if dm.AccessMode == "" {
				dm.AccessMode = accessMode.Name
			}
		}
	}

	return
}

//
// Build tasks.
func (r *Builder) Tasks(vmRef ref.Ref) (list []*plan.Task, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
	}
	for _, da := range vm.DiskAttachments {
		mB := da.Disk.ProvisionedSize / 0x100000
		list = append(
			list,
			&plan.Task{
				Name: da.Disk.ID,
				Progress: libitr.Progress{
					Total: mB,
				},
				Annotations: map[string]string{
					"unit": "MB",
				},
			})
	}

	return
}

//
//
func (r *Builder) TemplateLabels(vmRef ref.Ref) (labels map[string]string, err error) {
	vm := &model.Workload{}
	err = r.Source.Inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(
			err,
			"VM lookup failed.",
			"vm",
			vmRef.String())
		return
	}

	os, ok := osMap[vm.OSType]
	if !ok {
		if strings.Contains(vm.OSType, "linux") || strings.Contains(vm.OSType, "rhel") {
			os = DefaultLinux
		} else if strings.Contains(vm.OSType, "win") {
			os = DefaultWindows
		} else {
			os = Unknown
		}
	}

	labels = make(map[string]string)
	labels[fmt.Sprintf(TemplateOSLabel, os)] = "true"
	labels[TemplateWorkloadLabel] = "true"
	labels[TemplateFlavorLabel] = "true"

	return
}

//
// Return a stable identifier for a DataVolume.
func (r *Builder) ResolveDataVolumeIdentifier(dv *cdi.DataVolume) string {
	return dv.Spec.Source.Imageio.DiskID
}

func (r *Builder) Load() (err error) {
	return r.loadProvisioners()
}

//
// Load provisioner CRs.
func (r *Builder) loadProvisioners() (err error) {
	list := &api.ProvisionerList{}
	err = r.List(
		context.TODO(),
		list,
		&client.ListOptions{
			Namespace: r.Source.Provider.Namespace,
		},
	)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	r.provisioners = map[string]*api.Provisioner{}
	for i := range list.Items {
		p := &list.Items[i]
		r.provisioners[p.Spec.Name] = p
	}

	return
}
