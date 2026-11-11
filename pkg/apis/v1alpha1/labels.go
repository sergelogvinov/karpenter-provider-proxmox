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
	"crypto/sha256"
	"encoding/hex"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	TerminationFinalizer = apis.Group + "/termination"

	// LabelInstanceFamily is the instance family, e.g. c1, s1, m1, e2
	LabelInstanceFamily = apis.Group + "/instance-family" // c1, s1, m1, e2
	// LabelInstanceCPUType is the CPU type, e.g. host, kvm64, Broadwell, Skylake
	LabelInstanceCPUType = apis.Group + "/instance-cpu-type" // host, kvm64, Broadwell, Skylake
	// LabelInstanceCPU is the number of vCPUs
	LabelInstanceCPU = apis.Group + "/instance-cpu" // 2, 4, 8, ...
	// LabelInstanceMemory is the amount of memory in MiB
	LabelInstanceMemory = apis.Group + "/instance-memory" // 2048, 4096, 8192, ... (in MiB)
	// LabelInstanceImageID is the image ID
	LabelInstanceImageID = apis.Group + "/instance-image-id" // image ID
	// LabelReservationID is the reservation ID of a reserved offering: its region, zone and instance type
	LabelReservationID = apis.Group + "/reservation-id" // <region>-<zone>-<instancetype>

	// LabelBootstrapToken is the bootstrap token name used to join the node to the cluster
	LabelBootstrapToken = apis.Group + "/bootstrap-token" // bootstrap token

	// LabelNodeViewer is the label used by github.com/awslabs/eks-node-viewer
	LabelNodeViewer = "eks-node-viewer/instance-price"
)

func init() {
	karpv1.RestrictedLabelDomains = karpv1.RestrictedLabelDomains.Insert(apis.Group)
	karpv1.WellKnownLabels = karpv1.WellKnownLabels.Insert(
		LabelInstanceFamily,
		LabelInstanceCPUType,
		LabelInstanceCPU,
		LabelInstanceMemory,
		LabelInstanceImageID,
		LabelReservationID,
	)

	// Karpenter core tells reserved offerings apart by this label: a reserved offering requires its reservation ID,
	// every other offering requires the label not to exist.
	cloudprovider.ReservationIDLabel = LabelReservationID
	cloudprovider.ReservedCapacityLabels.Insert(LabelReservationID)
}

const (
	// maxLabelValueLength is the longest label value Kubernetes allows.
	maxLabelValueLength = 63
	// reservationIDHashLength is the hex characters of the SHA-256 that end a shortened reservation ID.
	reservationIDHashLength = 8
)

// ReservationID is the value of LabelReservationID for the reserved offering of instanceType in a zone of region:
// <region>-<zone>-<instancetype>, then "-" and the first reservationIDHashLength hex characters of the SHA-256 of
// <region>/<zone>/<instancetype>. Label values allow "-" and "." inside names, so only the hash, of fields joined by
// a character no label value has, tells apart IDs whose readable parts are the same. A readable part too long for
// a label value is cut short, and the hash kept.
func ReservationID(region, zone, instanceType string) string {
	sum := sha256.Sum256([]byte(region + "/" + zone + "/" + instanceType))
	hash := hex.EncodeToString(sum[:])[:reservationIDHashLength]

	id := region + "-" + zone + "-" + instanceType

	return id[:min(len(id), maxLabelValueLength-1-reservationIDHashLength)] + "-" + hash
}
