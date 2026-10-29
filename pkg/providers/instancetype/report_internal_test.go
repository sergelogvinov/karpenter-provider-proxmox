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
// where it is available, or where it is offered when it is available nowhere.
func TestOfferingsReportNamesZones(t *testing.T) {
	t.Parallel()

	c1 := &InstanceTypeStatic{Name: "c1.2VCPU-4GB"}
	c1.Offerings = cloudprovider.Offerings{
		zoneOffering(c1, "region-1", "pve-2", karpv1.CapacityTypeOnDemand, true),
		zoneOffering(c1, "region-1", "pve-1", karpv1.CapacityTypeOnDemand, true),
		zoneOffering(c1, "region-1", "pve-3", karpv1.CapacityTypeOnDemand, false),
	}

	n1 := &InstanceTypeStatic{Name: "n1.16VCPU-24GB"}
	n1.Offerings = cloudprovider.Offerings{
		zoneOffering(n1, "region-1", "pve-2", karpv1.CapacityTypeOnDemand, false),
		zoneOffering(n1, "region-1", "pve-1", karpv1.CapacityTypeOnDemand, false),
	}

	s1 := &InstanceTypeStatic{Name: "s1.8VCPU-32GB"}

	available, unavailable, offers := offeringsReport([]*InstanceTypeStatic{c1, n1, s1})

	require.Equal(t, map[string][]string{"c1.2VCPU-4GB": {"pve-1", "pve-2"}}, available)
	require.Equal(t, map[string][]string{"n1.16VCPU-24GB": {"pve-1", "pve-2"}, "s1.8VCPU-32GB": {}}, unavailable)
	require.Equal(t, 2, offers)
}
