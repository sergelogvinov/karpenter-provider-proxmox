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

package cloudcapacity

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/resources"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/zonecapacity"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ErrNoZoneFits means none of the candidate zones has room for the VM. An invalid
// request, such as a VM ID that is already reserved, is never ErrNoZoneFits.
var ErrNoZoneFits = errors.New("no zone has room for the VM")

// zoneFit is the fit of a VM in one candidate zone.
type zoneFit struct {
	zone string
	fit  zonecapacity.Fit
}

// span is how many NUMA nodes the VM would span, counting a shared VM as one,
// so a shared zone ranks with a pinned zone where the VM stays on one NUMA node.
func (f zoneFit) span() int {
	return max(f.fit.NUMANodes, 1)
}

func (p *DefaultProvider) ChooseZone(ctx context.Context, region string, zones []string, strategy *v1alpha1.PlacementStrategy, req resources.VMResources) (string, resources.VMResources, error) {
	log := log.FromContext(ctx).WithName("ChooseZone()").WithValues("region", region)

	p.muCapacityInfo.Lock()
	defer p.muCapacityInfo.Unlock()

	fits := []zoneFit{}
	errs := []error{}

	// failed records a zone's lack of room, and returns any other error to give up with. A zone whose
	// policy does not offer the VM, such as a NUMA slot in a zone that left static placement, has no room for it.
	failed := func(zone string, err error) error {
		err = fmt.Errorf("zone %s: %w", zone, err)
		if !errors.Is(err, zonecapacity.ErrNoRoom) && !errors.Is(err, zonecapacity.ErrNotOffered) {
			return err
		}

		errs = append(errs, err)

		return nil
	}

	for _, zone := range zones {
		info, ok := p.capacityInfo[zoneKey(region, zone)]
		if !ok || info.Capacity == nil || info.omitted {
			continue
		}

		fit, err := info.Capacity.Fit(req)
		if err != nil {
			if err := failed(zone, err); err != nil {
				return "", resources.VMResources{}, err
			}

			continue
		}

		fits = append(fits, zoneFit{zone: zone, fit: fit})
	}

	for _, f := range orderZoneFits(fits, strategy) {
		info := p.capacityInfo[zoneKey(region, f.zone)]

		reserved := req
		if err := info.Capacity.Reserve(&reserved); err != nil {
			if err := failed(f.zone, err); err != nil {
				return "", resources.VMResources{}, err
			}

			continue
		}

		log.V(1).Info("Capacity reserved in chosen zone", "zone", f.zone, "vmID", req.ID, "numaNodes", f.fit.NUMANodes, "leftover", f.fit.Leftover, "resourceStatus", info.Capacity.String())

		return f.zone, reserved, nil
	}

	if len(errs) == 0 {
		return "", resources.VMResources{}, fmt.Errorf("%w in region %s", ErrNoZoneFits, region)
	}

	return "", resources.VMResources{}, fmt.Errorf("%w in region %s: %w", ErrNoZoneFits, region, errors.Join(errs...))
}

// orderZoneFits orders fits by the NUMA nodes the VM would span, fewest first, then by strategy:
// least left over first for best fit, most left over first for balanced, and in random order
// for availability first. Ties go to the lowest zone name.
func orderZoneFits(fits []zoneFit, strategy *v1alpha1.PlacementStrategy) []zoneFit {
	fits = slices.Clone(fits)
	sort.Slice(fits, func(i, j int) bool { return fits[i].zone < fits[j].zone })

	// leftoverFirst reports whether leftover a goes before b; nil keeps the order.
	var leftoverFirst func(a, b float64) bool

	switch placementStrategy(strategy) {
	case v1alpha1.PlacementStrategyAvailabilityFirst:
		rand.Shuffle(len(fits), func(i, j int) { fits[i], fits[j] = fits[j], fits[i] })
	case v1alpha1.PlacementStrategyBestFit:
		leftoverFirst = func(a, b float64) bool { return a < b }
	default:
		leftoverFirst = func(a, b float64) bool { return a > b }
	}

	sort.SliceStable(fits, func(i, j int) bool {
		if fits[i].span() != fits[j].span() {
			return fits[i].span() < fits[j].span()
		}

		return leftoverFirst != nil && leftoverFirst(fits[i].fit.Leftover, fits[j].fit.Leftover)
	})

	return fits
}

// placementStrategy is the strategy to order zones by, Balanced when it is not set.
func placementStrategy(strategy *v1alpha1.PlacementStrategy) string {
	if strategy == nil || strategy.ZoneBalance == "" {
		return v1alpha1.PlacementStrategyBalanced
	}

	return strategy.ZoneBalance
}

// zoneKey is the key of a zone's capacity.
func zoneKey(region, zone string) string {
	return fmt.Sprintf("%s/%s", region, zone)
}
