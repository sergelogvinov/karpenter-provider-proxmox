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

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/validation"
)

// TestReservationID checks that a reservation ID is <region>-<zone>-<instancetype>, then "-" and the first 8 hex
// characters of the SHA-256 of <region>/<zone>/<instancetype>.
func TestReservationID(t *testing.T) {
	t.Parallel()

	// sha256("region-1/pve-1/r1.2VCPU-4GB") starts with b62af5c5.
	require.Equal(t, "region-1-pve-1-r1.2VCPU-4GB-b62af5c5", ReservationID("region-1", "pve-1", "r1.2VCPU-4GB"))
}

// TestReservationIDUnambiguous checks that fields whose joined names are the same get different reservation IDs.
func TestReservationIDUnambiguous(t *testing.T) {
	t.Parallel()

	require.NotEqual(t, ReservationID("prod-us", "west", "t"), ReservationID("prod", "us-west", "t"))
	require.NotEqual(t, ReservationID("region-1", "pve-1", "a.b"), ReservationID("region-1", "pve-1.a", "b"))
}

// TestLongReservationID checks that a reservation ID over 63 characters keeps its hash and cuts its readable part.
func TestLongReservationID(t *testing.T) {
	t.Parallel()

	id := ReservationID("region-1", "pve-1", "reserved-family-with-a-long-descriptive-name.2VCPU-4GB")

	// sha256("region-1/pve-1/reserved-family-with-a-long-descriptive-name.2VCPU-4GB") starts with 18256ab8.
	require.Equal(t, "region-1-pve-1-reserved-family-with-a-long-descriptive-18256ab8", id)
	require.Len(t, id, maxLabelValueLength)
	require.Empty(t, validation.IsValidLabelValue(id))
}
