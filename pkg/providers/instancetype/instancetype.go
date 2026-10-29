/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package instancetype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/samber/lo"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/operator/options"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider interface {
	List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error)
	ListWithFilter(ctx context.Context, filter func(*cloudprovider.InstanceType) bool) []*cloudprovider.InstanceType
	Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error)
	// CPUShare is the number of VMs that share the CPUs of instance type name: 2, 3 or 4.
	// Zero means dedicated CPUs, or an unknown instance type.
	CPUShare(name string) int
	// SlotFraction is the slot fraction of NUMA type name: 1, 2 or 4, from the NUMA template of its family.
	// It holds while the template is configured, whether or not the type is generated at the moment.
	// Zero means any other instance type, or an unknown one.
	SlotFraction(name string) int

	UpdateInstanceTypes(context.Context) error
	UpdateInstanceTypeOfferings(context.Context) error
}

type DefaultProvider struct {
	cloudCapacityProvider cloudcapacity.Provider
	// kubeClient reads the NodeClaims, whose NUMA types are kept while they use them.
	kubeClient client.Reader

	muInstanceTypes sync.RWMutex
	// instanceTypesInfo are the static types followed by the NUMA types generated on the last offerings refresh
	// and the kept NUMA types, those the NodeClaims still use.
	instanceTypesInfo []*InstanceTypeStatic
	// staticTypes are the instance types as configured.
	staticTypes []*InstanceTypeStatic
	// numaTemplates are the configured NUMA templates, which the NUMA types are generated from.
	numaTemplates []*InstanceTypeStatic
	// staticNames are the names of the static types.
	staticNames map[string]bool
	// cpuShares are the CPU shares of the instance types that have one, by name.
	cpuShares map[string]int
	// slotFractions are the slot fractions of the NUMA templates, by family.
	slotFractions map[string]int
	// nodeClaimUses are the instance types the NodeClaims used when they were last listed.
	nodeClaimUses []nodeClaimUse

	log logr.Logger
}

func NewDefaultProvider(ctx context.Context, cloudCapacityProvider cloudcapacity.Provider, kubeClient client.Reader) *DefaultProvider {
	log := log.FromContext(ctx).WithName("instancetype")

	p := &DefaultProvider{
		cloudCapacityProvider: cloudCapacityProvider,
		kubeClient:            kubeClient,
		log:                   log,
	}
	p.setInstanceTypes(loadDefaultInstanceTypes())

	return p
}

func (p *DefaultProvider) List(ctx context.Context, nodeClass *v1alpha1.ProxmoxNodeClass) ([]*cloudprovider.InstanceType, error) {
	return p.ListWithFilter(ctx, func(_ *cloudprovider.InstanceType) bool {
		return true
	}), nil
}

func (p *DefaultProvider) ListWithFilter(ctx context.Context, filter func(*cloudprovider.InstanceType) bool) []*cloudprovider.InstanceType {
	p.muInstanceTypes.RLock()
	defer p.muInstanceTypes.RUnlock()

	filtered := []*cloudprovider.InstanceType{}

	for _, item := range p.instanceTypesInfo {
		instanceType := &cloudprovider.InstanceType{
			Name:      item.Name,
			Offerings: item.Offerings.DeepCopy(),
			Capacity:  item.Capacity.DeepCopy(),
			Overhead:  copyOverhead(item.Overhead),
		}

		if filter(instanceType) {
			instanceType.Requirements = computeRequirements(item.Name, instanceType.Offerings, p.cloudCapacityProvider.Regions())

			filtered = append(filtered, instanceType)
		}
	}

	return filtered
}

// copyOverhead is a deep copy of overhead, empty if overhead is nil.
func copyOverhead(overhead *cloudprovider.InstanceTypeOverhead) *cloudprovider.InstanceTypeOverhead {
	if overhead == nil {
		return &cloudprovider.InstanceTypeOverhead{}
	}

	return &cloudprovider.InstanceTypeOverhead{
		KubeReserved:      overhead.KubeReserved.DeepCopy(),
		SystemReserved:    overhead.SystemReserved.DeepCopy(),
		EvictionThreshold: overhead.EvictionThreshold.DeepCopy(),
	}
}

func (p *DefaultProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	filtered := p.ListWithFilter(ctx, func(item *cloudprovider.InstanceType) bool {
		return item.Name == name
	})

	if len(filtered) == 0 {
		return nil, fmt.Errorf("instance type not found: %s", name)
	}

	return filtered[0], nil
}

func (p *DefaultProvider) CPUShare(name string) int {
	p.muInstanceTypes.RLock()
	defer p.muInstanceTypes.RUnlock()

	return p.cpuShares[name]
}

func (p *DefaultProvider) SlotFraction(name string) int {
	p.muInstanceTypes.RLock()
	defer p.muInstanceTypes.RUnlock()

	// A static type keeps its name, even in the family of a NUMA template.
	if p.staticNames[name] {
		return 0
	}

	return p.slotFractions[instanceFamily(name)]
}

// VMRequest is the VM request for an instance type of capacity, seated in a CPU share group
// of cpuShare seats unless it is 0, and in a strict NUMA slot of slotFraction unless it is 0:
// a NUMA type's slot shape is its slot fraction with its CPUs and memory.
func VMRequest(capacity corev1.ResourceList, cpuShare, slotFraction int) resources.VMResources {
	req := resources.VMResources{
		CPUs:     int(capacity.Cpu().Value()),
		Memory:   uint64(capacity.Memory().Value()),
		CPUShare: cpuShare,
	}

	if slotFraction != 0 {
		req.Slot = &zonecapacity.SlotShape{Fraction: slotFraction, CPUs: req.CPUs, Memory: req.Memory}
	}

	return req
}

// instanceFamily is the family of instance type name, the part before the first dot.
func instanceFamily(name string) string {
	family, _, _ := strings.Cut(name, ".")

	return family
}

func (p *DefaultProvider) UpdateInstanceTypes(ctx context.Context) error {
	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	if name := options.FromContext(ctx).InstanceTypesFilePath; name != "" {
		instanceTypes, err := loadInstanceTypesFromFile(name)
		if err != nil {
			return err
		}

		p.setInstanceTypes(instanceTypes)
	}

	return nil
}

func (p *DefaultProvider) UpdateInstanceTypeOfferings(ctx context.Context) error {
	log := p.log.WithName("UpdateInstanceTypeOfferings()")

	// When the NodeClaims cannot be listed, the refresh still runs on the uses last listed, and returns the error.
	uses, listErr := p.listNodeClaimUses(ctx)

	p.muInstanceTypes.Lock()
	defer p.muInstanceTypes.Unlock()

	if listErr == nil {
		p.nodeClaimUses = uses
	}

	regions := p.cloudCapacityProvider.Regions()

	for _, item := range p.staticTypes {
		p.createOfferings(item, regions)
	}

	// The NUMA types are rebuilt from the current slot shapes, and kept where NodeClaims still use them;
	// the static types are kept as they are.
	p.instanceTypesInfo = append(slices.Clone(p.staticTypes), p.keepNUMATypesInUse(p.numaTypes(regions), p.nodeClaimUses)...)

	available, unavailable, offers := offeringsReport(p.instanceTypesInfo)

	log.V(1).Info("Instance type offerings updated", "instanceTypes", len(p.instanceTypesInfo), "offers", offers)
	log.V(1).Info("Instance type offerings report", "available", available, "unavailable", unavailable)

	return listErr
}

// offeringsReport returns the zones where each instance type is available and, for the instance types available
// nowhere, the zones where they are offered, both sorted, with the number of available offerings.
func offeringsReport(instanceTypes []*InstanceTypeStatic) (available, unavailable map[string][]string, offers int) {
	available = make(map[string][]string, len(instanceTypes))
	unavailable = map[string][]string{}

	zones := func(offerings cloudprovider.Offerings) []string {
		names := lo.Map(offerings, func(o *cloudprovider.Offering, _ int) string {
			return o.Requirements.Get(corev1.LabelTopologyZone).Any()
		})
		slices.Sort(names)

		return slices.Compact(names)
	}

	for _, item := range instanceTypes {
		offerings := item.Offerings.Available()
		offers += len(offerings)

		if len(offerings) > 0 {
			available[item.Name] = zones(offerings)
		} else {
			unavailable[item.Name] = zones(item.Offerings)
		}
	}

	return available, unavailable, offers
}

// setInstanceTypes replaces the instance types and NUMA templates, with the lookups of the static names,
// their CPU shares, and the templates' slot fractions.
// The NUMA types are generated on the next offerings refresh. The caller holds the lock.
func (p *DefaultProvider) setInstanceTypes(instanceTypes []*InstanceTypeStatic) {
	p.staticTypes = []*InstanceTypeStatic{}
	p.numaTemplates = []*InstanceTypeStatic{}
	p.staticNames = make(map[string]bool, len(instanceTypes))
	p.cpuShares = make(map[string]int, len(instanceTypes))
	p.slotFractions = map[string]int{}

	for _, item := range instanceTypes {
		if item.NUMA != 0 {
			p.numaTemplates = append(p.numaTemplates, item)
			p.slotFractions[item.Name] = item.NUMA

			continue
		}

		p.staticTypes = append(p.staticTypes, item)
		p.staticNames[item.Name] = true

		if item.CPUShare != 0 {
			p.cpuShares[item.Name] = item.CPUShare
		}
	}

	p.instanceTypesInfo = slices.Clone(p.staticTypes)
}

func priceFromResources(resources corev1.ResourceList) float64 {
	// Let's assume the price is electricity cost
	price := 0.0

	for k, v := range resources {
		switch k { //nolint:exhaustive
		case corev1.ResourceCPU:
			price += 0.025 * v.AsApproximateFloat64()
		case corev1.ResourceMemory:
			price += 0.001 * v.AsApproximateFloat64() / 1e9
		}
	}

	return price
}

// priceOf is the price of an instance type: a CPU share type pays for vCPU/N CPUs and its full memory.
func priceOf(opts *InstanceTypeStatic) float64 {
	if opts.CPUShare == 0 {
		return priceFromResources(opts.Capacity)
	}

	capacity := opts.Capacity.DeepCopy()
	capacity[corev1.ResourceCPU] = *resource.NewMilliQuantity(opts.Capacity.Cpu().MilliValue()/int64(opts.CPUShare), resource.DecimalSI)

	return priceFromResources(capacity)
}

func (p *DefaultProvider) createOfferings(opts *InstanceTypeStatic, regions []string) {
	opts.Offerings = []*cloudprovider.Offering{}

	for _, region := range regions {
		for _, zone := range p.cloudCapacityProvider.Zones(region) {
			err := p.cloudCapacityProvider.FitInZone(region, zone, VMRequest(opts.Capacity, opts.CPUShare, 0))
			if errors.Is(err, zonecapacity.ErrNotOffered) {
				continue
			}

			opts.Offerings = append(opts.Offerings, zoneOfferings(opts, region, zone, err == nil)...)
		}
	}
}

// zoneOfferings are the offerings of instance type it in one zone, one per capacity type.
func zoneOfferings(it *InstanceTypeStatic, region, zone string, available bool) []*cloudprovider.Offering {
	offerings := []*cloudprovider.Offering{}

	// We use capacityType array to allow multiple capacity types per instance type in the future
	for _, ct := range lo.Uniq([]string{it.CapacityType}) {
		offerings = append(offerings, zoneOffering(it, region, zone, ct, available))
	}

	return offerings
}

// zoneOffering is the offering of instance type it in one zone for capacity type ct.
func zoneOffering(it *InstanceTypeStatic, region, zone, ct string, available bool) *cloudprovider.Offering {
	price := priceOf(it)

	return &cloudprovider.Offering{
		Price:     lo.Ternary(ct == karpv1.CapacityTypeSpot, price*.5, price),
		Available: available,
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, it.Name),
			scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
			scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, ct),
			scheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, instanceFamily(it.Name)),
		),
	}
}

func computeRequirements(instanceTypeName string, offerings cloudprovider.Offerings, regions []string) scheduling.Requirements {
	requirements := scheduling.NewRequirements(
		// Well Known Upstream
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, instanceTypeName),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, string(karpv1.ArchitectureAmd64)),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
		scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, regions...),
		// scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, lo.Map(offerings.Available(), func(o *cloudprovider.Offering, _ int) string {
		// 	return o.Requirements.Get(corev1.LabelTopologyZone).Any()
		// })...),

		// Well Known to Karpenter
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, lo.Map(offerings.Available(), func(o *cloudprovider.Offering, _ int) string {
			return o.Requirements.Get(karpv1.CapacityTypeLabelKey).Any()
		})...),

		// Well Known to Proxmox
		scheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, instanceFamily(instanceTypeName)),
		scheduling.NewRequirement(v1alpha1.LabelInstanceCPUType, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceImageID, corev1.NodeSelectorOpDoesNotExist),
	)

	return requirements
}

func loadInstanceTypesFromFile(name string) ([]*InstanceTypeStatic, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("failed to read instance types file %s: %w", name, err)
	}

	instanceTypeStatic := []*InstanceTypeStatic{}

	if err := json.Unmarshal(data, &instanceTypeStatic); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instance types %s: %w", name, err)
	}

	templates := map[string]bool{}

	for _, i := range instanceTypeStatic {
		if i.CPUShare != 0 && !zonecapacity.ValidCPUShare(i.CPUShare) {
			return nil, fmt.Errorf("instance type %s in %s: cpushare %d must be 0 (dedicated CPUs), %s", i.Name, name, i.CPUShare, zonecapacity.CPUShareSeats)
		}

		if i.NUMA != 0 && !zonecapacity.ValidSlotFraction(i.NUMA) {
			return nil, fmt.Errorf("instance type %s in %s: numa %d must be 0 (not a NUMA template), %s", i.Name, name, i.NUMA, zonecapacity.SlotFractions)
		}

		if i.NUMA != 0 && i.CPUShare != 0 {
			return nil, fmt.Errorf("instance type %s in %s: a NUMA template cannot have a cpushare", i.Name, name)
		}

		if i.NUMA != 0 {
			if templates[i.Name] {
				return nil, fmt.Errorf("instance type %s in %s: duplicate NUMA template name", i.Name, name)
			}

			templates[i.Name] = true
		}

		if i.CapacityType == "" {
			i.CapacityType = karpv1.CapacityTypeOnDemand
		}
	}

	return instanceTypeStatic, nil
}

func loadDefaultInstanceTypes() []*InstanceTypeStatic {
	instanceTypes := []*InstanceTypeStatic{}

	options := InstanceTypeOptions{
		CPUs:              []int{1, 2, 4, 8, 16},
		MemFactors:        []int{2, 3, 4, 8},
		Storage:           30,
		NUMA:              []int{1, 2},
		KubeletOverhead:   true,
		SystemOverhead:    true,
		EvictionThreshold: true,
	}

	// The options are constant and valid, which the unit tests check through NewDefaultProvider.
	generated := lo.Must(options.Generate())

	for _, i := range generated {
		instance := InstanceTypeStatic{
			Name:         i.Name,
			Capacity:     i.Capacity,
			CapacityType: i.CapacityType,
			Overhead:     copyOverhead(i.Overhead),
			NUMA:         i.NUMA,
		}

		// A NUMA template has no overhead of its own: it is computed for each NUMA type.
		if i.NUMA != 0 {
			instance.Overhead = nil
		}

		instanceTypes = append(instanceTypes, &instance)
	}

	return instanceTypes
}
