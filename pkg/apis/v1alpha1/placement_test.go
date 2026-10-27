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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTemplatePlacement(t *testing.T) {
	t.Parallel()

	p, err := ParseTemplatePlacement("region-1/pve-1/1000")
	require.NoError(t, err)
	assert.Equal(t, TemplatePlacement{Region: "region-1", Zone: "pve-1", TemplateID: 1000}, p)
	assert.Equal(t, "region-1/pve-1/1000", p.String())

	for _, s := range []string{
		"",
		"region-1",
		"region-1/pve-1",
		"region-1//1000",
		"/pve-1/1000",
		"region-1/pve-1/",
		"region-1/pve-1/0",
		"region-1/pve-1/-1",
		"region-1/pve-1/abc",
		"region-1/pve-1/1000/extra",
	} {
		_, err := ParseTemplatePlacement(s)
		assert.Error(t, err, s)
	}
}

func TestTemplatePlacementsSkipMalformed(t *testing.T) {
	t.Parallel()

	template := &ProxmoxTemplate{}
	template.Status.Zones = []string{"region-1/pve-1/1000", "region-1/pve-2", "region-2/pve-3/abc", "region-2/pve-3/2000"}

	assert.Equal(t, []TemplatePlacement{
		{Region: "region-1", Zone: "pve-1", TemplateID: 1000},
		{Region: "region-2", Zone: "pve-3", TemplateID: 2000},
	}, template.TemplatePlacements())
}

func TestNodeClassTemplatePlacementsByRegion(t *testing.T) {
	t.Parallel()

	nodeClass := &ProxmoxNodeClass{}
	nodeClass.SetTemplatePlacements([]TemplatePlacement{
		{Region: "region-1", Zone: "pve-1", TemplateID: 1000},
		{Region: "region-10", Zone: "pve-2", TemplateID: 1001},
		{Region: "region-1", Zone: "pve-3", TemplateID: 1002},
	})

	assert.Equal(t, []string{"region-1/pve-1/1000", "region-10/pve-2/1001", "region-1/pve-3/1002"}, nodeClass.Status.SelectedZones)
	assert.Equal(t, []TemplatePlacement{
		{Region: "region-1", Zone: "pve-1", TemplateID: 1000},
		{Region: "region-1", Zone: "pve-3", TemplateID: 1002},
	}, nodeClass.TemplatePlacements("region-1"))

	nodeClass.SetTemplatePlacements(nil)
	assert.Nil(t, nodeClass.Status.SelectedZones)
}
