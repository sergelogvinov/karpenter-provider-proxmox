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

package instance

import (
	"math"
	"math/rand/v2"
	"sort"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

func (p *DefaultProvider) sortBestZoneByPlacementStrategy(placementStrategy *v1alpha1.PlacementStrategy, region string, zones []string) []string {
	if len(zones) == 1 {
		return zones
	}

	strategy := placementStrategy
	if strategy == nil {
		strategy = &v1alpha1.PlacementStrategy{
			ZoneBalance: v1alpha1.PlacementStrategyBalanced,
		}
	}

	switch strategy.ZoneBalance {
	case v1alpha1.PlacementStrategyAvailabilityFirst:
		// Sort zones randomly to prioritize availability
		sortedZones := make([]string, len(zones))
		for i, v := range rand.Perm(len(zones)) {
			sortedZones[v] = zones[i]
		}

		return sortedZones
	default:
		// Sort zones by CPU load
		return p.cloudCapacityProvider.SortZonesByCPULoad(region, zones)
	}
}

func orderInstanceTypesByPrice(instanceTypes []*cloudprovider.InstanceType, requirements scheduling.Requirements) []*cloudprovider.InstanceType {
	// Order instance types so that we get the cheapest instance types of the available offerings
	sort.Slice(instanceTypes, func(i, j int) bool {
		iPrice := math.MaxFloat64
		jPrice := math.MaxFloat64

		if len(instanceTypes[i].Offerings.Available().Compatible(requirements)) > 0 {
			iPrice = instanceTypes[i].Offerings.Available().Compatible(requirements).Cheapest().Price
		}

		if len(instanceTypes[j].Offerings.Available().Compatible(requirements)) > 0 {
			jPrice = instanceTypes[j].Offerings.Available().Compatible(requirements).Cheapest().Price
		}

		if iPrice == jPrice {
			return instanceTypes[i].Name < instanceTypes[j].Name
		}

		return iPrice < jPrice
	})

	return instanceTypes
}
