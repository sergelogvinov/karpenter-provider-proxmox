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
	"fmt"
	"strconv"
	"strings"
)

// TemplatePlacement is one VM template on one zone of one region. Status
// fields store it as "region/zone/vmid".
type TemplatePlacement struct {
	Region     string
	Zone       string
	TemplateID uint64
}

// String returns the placement in its status form, "region/zone/vmid".
func (p TemplatePlacement) String() string {
	return fmt.Sprintf("%s/%s/%d", p.Region, p.Zone, p.TemplateID)
}

// ParseTemplatePlacement parses a placement in its status form,
// "region/zone/vmid". Region and zone must be non-empty and vmid a
// positive integer.
func ParseTemplatePlacement(s string) (TemplatePlacement, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return TemplatePlacement{}, fmt.Errorf("invalid template placement %q: want region/zone/vmid", s)
	}

	id, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || id == 0 {
		return TemplatePlacement{}, fmt.Errorf("invalid template placement %q: vmid must be a positive integer", s)
	}

	return TemplatePlacement{Region: parts[0], Zone: parts[1], TemplateID: id}, nil
}

// parseTemplatePlacements parses entries, skipping malformed ones. A
// non-empty region keeps only that region's placements.
func parseTemplatePlacements(entries []string, region string) []TemplatePlacement {
	placements := make([]TemplatePlacement, 0, len(entries))

	for _, entry := range entries {
		p, err := ParseTemplatePlacement(entry)
		if err != nil || (region != "" && p.Region != region) {
			continue
		}

		placements = append(placements, p)
	}

	return placements
}

// formatTemplatePlacements returns placements in their status form, or nil
// when there are none.
func formatTemplatePlacements(placements []TemplatePlacement) []string {
	if len(placements) == 0 {
		return nil
	}

	entries := make([]string, 0, len(placements))
	for _, p := range placements {
		entries = append(entries, p.String())
	}

	return entries
}
