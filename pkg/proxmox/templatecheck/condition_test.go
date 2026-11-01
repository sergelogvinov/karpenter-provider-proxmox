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

package templatecheck

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"
)

// TestZoneOfferingNothingIsIncompatible checks that intended splits which together drop every kind in a zone
// make the condition false. The hugepages rule alone never does this; a later rule can.
func TestZoneOfferingNothingIsIncompatible(t *testing.T) {
	t.Parallel()

	id := ZoneID{Region: "region-1", Zone: "pve-1"}
	r := Result{drops: []Drop{
		{Zone: id, Kind: NUMATypes, Reason: ReasonHugepagesUnset, Split: true, Message: "a"},
		{Zone: id, Kind: NonNUMATypes, Reason: "SomeOtherSplit", Split: true, Message: "b"},
	}}

	nodeClass := &v1alpha1.ProxmoxNodeClass{}
	r.SetCondition(nodeClass)

	cond := nodeClass.StatusConditions().Get(v1alpha1.ConditionNodeClassTemplateCompatible)
	require.True(t, cond.IsFalse())
	require.Equal(t, ReasonOffersNothing, cond.Reason)
}
