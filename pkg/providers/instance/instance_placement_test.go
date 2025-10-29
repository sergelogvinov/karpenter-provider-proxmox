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
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

func TestGetValuesByKey(t *testing.T) {
	opIn := corev1.NodeSelectorOpIn

	tests := []struct {
		name         string
		instanceType *cloudprovider.InstanceType
		defaults     []string
		expect       []string
	}{
		{
			name: "no-restrictions",
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available:    true,
						Price:        1.0,
						Requirements: scheduling.NewRequirements(),
					},
					&cloudprovider.Offering{
						Available:    true,
						Price:        2.0,
						Requirements: scheduling.NewRequirements(),
					},
				},
			},
			defaults: []string{"us-west-1", "us-east-1"},
			expect:   []string{"us-west-1", "us-east-1"},
		},
		{
			name: "with-restriction",
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-west-1"),
						),
					},
					&cloudprovider.Offering{
						Available: true,
						Price:     2.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-west-1"),
						),
					},
				},
			},
			defaults: []string{"us-west-1", "us-east-1"},
			expect:   []string{"us-west-1"},
		},
		{
			name: "with-different-restriction",
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-west-1"),
						),
					},
					&cloudprovider.Offering{
						Available: true,
						Price:     2.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-east-1"),
						),
					},
				},
			},
			defaults: []string{"us-west-1", "us-east-1"},
			expect:   []string{"us-west-1", "us-east-1"},
		},
		{
			name: "with-different-regions",
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-west-1"),
						),
					},
					&cloudprovider.Offering{
						Available: true,
						Price:     2.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-east-2"),
						),
					},
				},
			},
			defaults: []string{"us-west-1", "us-east-1"},
			expect:   []string{"us-west-1"},
		},
		{
			name: "empty-regions",
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-west-1"),
						),
					},
					&cloudprovider.Offering{
						Available: true,
						Price:     2.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(corev1.LabelTopologyRegion, opIn, "us-east-2"),
						),
					},
				},
			},
			defaults: []string{},
			expect:   []string{"us-west-1", "us-east-2"},
		},
	}

	for _, tt := range tests {
		res := getValuesByKey(tt.instanceType, corev1.LabelTopologyRegion, tt.defaults)

		assert.Equal(t, tt.expect, res, tt.name)
	}
}

// nolint:dupl
func TestGetCapacityType(t *testing.T) {
	opIn := corev1.NodeSelectorOpIn

	tests := []struct {
		name         string
		nodeClaim    *karpv1.NodeClaim
		instanceType *cloudprovider.InstanceType
		region       string
		zone         string
		expect       string
	}{
		{
			name: "spot-available",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyRegion, Operator: opIn, Values: []string{"us-west-1"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyZone, Operator: opIn, Values: []string{"us-west-1a", "us-west-1b"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: karpv1.CapacityTypeLabelKey, Operator: opIn, Values: []string{karpv1.CapacityTypeSpot, karpv1.CapacityTypeOnDemand}}},
					},
				},
			},
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, opIn, karpv1.CapacityTypeSpot),
						),
					},
				},
			},
			region: "us-west-1",
			zone:   "us-west-1a",
			expect: karpv1.CapacityTypeSpot,
		},
		{
			name: "spot-unavailable-use-on-demand",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyRegion, Operator: opIn, Values: []string{"us-west-1"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyZone, Operator: opIn, Values: []string{"us-west-1a", "us-west-1b"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: karpv1.CapacityTypeLabelKey, Operator: opIn, Values: []string{karpv1.CapacityTypeSpot, karpv1.CapacityTypeOnDemand}}},
					},
				},
			},
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, opIn, karpv1.CapacityTypeOnDemand),
						),
					},
				},
			},
			region: "us-west-1",
			zone:   "us-west-1a",
			expect: karpv1.CapacityTypeOnDemand,
		},
		{
			name: "no-capacity-type-requested-use-on-demand",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyRegion, Operator: opIn, Values: []string{"us-west-1"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyZone, Operator: opIn, Values: []string{"us-west-1a", "us-west-1b"}}},
					},
				},
			},
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, opIn, karpv1.CapacityTypeSpot),
						),
					},
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, opIn, karpv1.CapacityTypeOnDemand),
						),
					},
				},
			},
			region: "us-west-1",
			zone:   "us-west-1a",
			expect: karpv1.CapacityTypeOnDemand,
		},
		{
			name: "reserved-available",
			nodeClaim: &karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyRegion, Operator: opIn, Values: []string{"us-west-1"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: corev1.LabelTopologyZone, Operator: opIn, Values: []string{"us-west-1a", "us-west-1b"}}},
						{NodeSelectorRequirement: corev1.NodeSelectorRequirement{Key: karpv1.CapacityTypeLabelKey, Operator: opIn, Values: []string{karpv1.CapacityTypeReserved, karpv1.CapacityTypeSpot}}},
					},
				},
			},
			instanceType: &cloudprovider.InstanceType{
				Name: "type-1",
				Offerings: cloudprovider.Offerings{
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, opIn, karpv1.CapacityTypeOnDemand),
						),
					},
					&cloudprovider.Offering{
						Available: true,
						Price:     1.0,
						Requirements: scheduling.NewRequirements(
							scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, opIn, karpv1.CapacityTypeReserved),
						),
					},
				},
			},
			region: "us-west-1",
			zone:   "us-west-1a",
			expect: karpv1.CapacityTypeReserved,
		},
	}

	for _, tt := range tests {
		res := getCapacityType(tt.nodeClaim, tt.instanceType, tt.region, tt.zone)

		assert.Equal(t, tt.expect, res, tt.name)
	}
}
