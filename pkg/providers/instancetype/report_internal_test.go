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
	"testing"

	"github.com/stretchr/testify/require"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// TestOfferingsReportNamesZones checks that the offerings report names the zones of each instance type:
// where it is available, grouped by capacity type, or where it is offered when it is available nowhere.
func TestOfferingsReportNamesZones(t *testing.T) {
	t.Parallel()

	// offering is the offering of it in zone for capacity type ct.
	offering := func(it *InstanceTypeStatic, zone, ct string, available bool) *cloudprovider.Offering {
		o := zoneOffering(it, "region-1", zone, ct)
		o.Available = available

		return o
	}

	c1 := &InstanceTypeStatic{Name: "c1.2VCPU-4GB"}
	c1.Offerings = cloudprovider.Offerings{
		offering(c1, "pve-2", karpv1.CapacityTypeOnDemand, true),
		offering(c1, "pve-1", karpv1.CapacityTypeOnDemand, true),
		offering(c1, "pve-3", karpv1.CapacityTypeOnDemand, false),
	}

	r1 := &InstanceTypeStatic{Name: "r1.4VCPU-8GB"}
	r1.Offerings = cloudprovider.Offerings{
		offering(r1, "pve-3", karpv1.CapacityTypeReserved, true),
		offering(r1, "pve-1", karpv1.CapacityTypeReserved, true),
	}

	n1 := &InstanceTypeStatic{Name: "n1.16VCPU-24GB"}
	n1.Offerings = cloudprovider.Offerings{
		offering(n1, "pve-2", karpv1.CapacityTypeOnDemand, false),
		offering(n1, "pve-1", karpv1.CapacityTypeOnDemand, false),
	}

	s1 := &InstanceTypeStatic{Name: "s1.8VCPU-32GB"}

	available, unavailable, offers := offeringsReport([]*InstanceTypeStatic{c1, r1, n1, s1})

	require.Equal(t, map[string]map[string][]string{
		karpv1.CapacityTypeOnDemand: {"c1.2VCPU-4GB": {"pve-1", "pve-2"}},
		karpv1.CapacityTypeReserved: {"r1.4VCPU-8GB": {"pve-1", "pve-3"}},
	}, available)
	require.Equal(t, map[string][]string{"n1.16VCPU-24GB": {"pve-1", "pve-2"}, "s1.8VCPU-32GB": {}}, unavailable)
	require.Equal(t, 4, offers)
}
