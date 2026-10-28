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
	"io"
	"os"
	"testing"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	goYaml "sigs.k8s.io/yaml/goyaml.v3"
)

func readISOFile(t *testing.T, isoPath string) map[string]string {
	t.Helper()

	bk, err := file.OpenFromPath(isoPath, true)
	require.NoError(t, err)

	defer bk.Close()

	fs, err := iso9660.Read(bk, 0, 0, cloudInitISOBlockSize)
	require.NoError(t, err)

	entries, err := fs.ReadDir(".")
	require.NoError(t, err)

	contents := make(map[string]string, len(entries))

	for _, entry := range entries {
		f, err := fs.OpenFile("/"+entry.Name(), os.O_RDONLY)
		require.NoError(t, err)

		data, err := io.ReadAll(f)
		require.NoError(t, err)

		contents[entry.Name()] = string(data)
	}

	return contents
}

func TestBuildCloudInitISO(t *testing.T) {
	t.Parallel()

	isoPath, cleanup, err := buildCloudInitISO("build-cloud-init-iso-test.iso", cloudInitFiles{
		UserData:      "user-data-content",
		MetaData:      "meta-data-content",
		VendorData:    "vendor-data-content",
		NetworkConfig: "network-config-content",
	})
	defer cleanup()

	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"user-data":      "user-data-content",
		"meta-data":      "meta-data-content",
		"vendor-data":    "vendor-data-content",
		"network-config": "network-config-content",
	}, readISOFile(t, isoPath))
}

func TestBuildCloudInitISOOmitsEmptyOptionalFiles(t *testing.T) {
	t.Parallel()

	isoPath, cleanup, err := buildCloudInitISO("build-cloud-init-iso-test-minimal.iso", cloudInitFiles{
		UserData: "user-data-content",
		MetaData: "meta-data-content",
	})
	defer cleanup()

	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"user-data": "user-data-content",
		"meta-data": "meta-data-content",
	}, readISOFile(t, isoPath))
}

func testCloudInitInputs() cloudInitInputs {
	return cloudInitInputs{
		NodeClaim: &karpv1.NodeClaim{Name: "node-1"},
		NodeClass: &v1alpha1.ProxmoxNodeClass{
			Name: "default",
			Spec: v1alpha1.ProxmoxNodeClassSpec{Tags: []string{"k8s"}},
		},
		InstanceType: &cloudprovider.InstanceType{
			Name: "t1.small",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
		Region:       "region-1",
		Zone:         "pve-1",
		VMID:         100,
		InstanceUUID: "uuid-1",
	}
}

// renderKubeletConfiguration renders inputs with a user-data template that
// dumps the kubelet configuration, and decodes it back.
func renderKubeletConfiguration(t *testing.T, inputs cloudInitInputs) KubeletConfiguration {
	t.Helper()

	inputs.Templates = map[string][]byte{"user-data": []byte("{{ toYaml .Kubernetes.KubeletConfiguration }}")}

	files, err := renderCloudInit(inputs)
	require.NoError(t, err)

	cfg := KubeletConfiguration{}
	require.NoError(t, goYaml.Unmarshal([]byte(files.UserData), &cfg))

	return cfg
}

func TestRenderCloudInitDefaultTemplates(t *testing.T) {
	t.Parallel()

	files, err := renderCloudInit(testCloudInitInputs())
	require.NoError(t, err)

	assert.Contains(t, files.UserData, "#cloud-config\nhostname: node-1\n")
	assert.Equal(t, `hostname: node-1
local-hostname: node-1
instance-id: 100
instance-type: t1.small
provider-id: proxmox://region-1/100
region: region-1
zone: pve-1
availability-zone: pve-1
`, files.MetaData)
	assert.Empty(t, files.VendorData)
	assert.NotEmpty(t, files.NetworkConfig)
}

func TestRenderCloudInitCustomTemplates(t *testing.T) {
	t.Parallel()

	inputs := testCloudInitInputs()
	inputs.KubernetesVersion = "v1.34.0"
	inputs.BootstrapToken = "abcdef.0123456789abcdef"
	inputs.Values = map[string]string{"Cluster": "prod"}
	inputs.Templates = map[string][]byte{
		"user-data":      []byte("{{ .Kubernetes.Version }} {{ .Kubernetes.BootstrapToken }} {{ .Values.Cluster }} {{ .Resources.CPU }}"),
		"meta-data":      []byte("{{ .InstanceUUID }} {{ .NodeClass }} {{ .Tags }}"),
		"vendor-data":    []byte("{{ .ProviderID }}"),
		"network-config": []byte("static"),
	}

	files, err := renderCloudInit(inputs)
	require.NoError(t, err)

	assert.Equal(t, cloudInitFiles{
		UserData:      "v1.34.0 abcdef.0123456789abcdef prod 2",
		MetaData:      "uuid-1 default [k8s]",
		VendorData:    "proxmox://region-1/100",
		NetworkConfig: "static",
	}, files)
}

func TestRenderCloudInitTemplateError(t *testing.T) {
	t.Parallel()

	inputs := testCloudInitInputs()
	inputs.Templates = map[string][]byte{"meta-data": []byte("{{ .Missing }")}

	_, err := renderCloudInit(inputs)
	assert.ErrorContains(t, err, "metadata template")
}

func TestRenderCloudInitKubeletConfiguration(t *testing.T) {
	t.Parallel()

	// Subtests run in parallel after this function returns, so the check
	// that no render changed the defaults has to wait for them.
	t.Cleanup(func() {
		assert.Equal(t, "100Mi", DefaultEvictionHard["memory.available"], "rendering must not change the default eviction thresholds")
	})

	unregistered := []KubernetesTaint{{Key: karpv1.UnregisteredTaintKey, Effect: corev1.TaintEffectNoExecute}}

	tests := []struct {
		name     string
		mutate   func(*cloudInitInputs)
		expected KubeletConfiguration
	}{
		{
			name: "defaults",
			expected: KubeletConfiguration{
				ProviderID:         "proxmox://region-1/100",
				RegisterWithTaints: unregistered,
			},
		},
		{
			name: "node class settings and instance type overhead",
			mutate: func(in *cloudInitInputs) {
				in.NodeClass.Spec.KubeletConfiguration = &v1alpha1.KubeletConfiguration{CPUManagerPolicy: "static"}
				in.InstanceType.Overhead = &cloudprovider.InstanceTypeOverhead{
					KubeReserved:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
					SystemReserved:    corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
					EvictionThreshold: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("200Mi")},
				}
			},
			expected: KubeletConfiguration{
				CPUManagerPolicy: "static",
				ProviderID:       "proxmox://region-1/100",
				KubeReserved:     map[string]string{"cpu": "100m"},
				SystemReserved:   map[string]string{"memory": "256Mi"},
				EvictionHard: map[string]string{
					"memory.available":  "200Mi",
					"nodefs.available":  "10%",
					"imagefs.available": "15%",
				},
				RegisterWithTaints: unregistered,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inputs := testCloudInitInputs()
			if tt.mutate != nil {
				tt.mutate(&inputs)
			}

			assert.Equal(t, tt.expected, renderKubeletConfiguration(t, inputs))
		})
	}
}

func TestRenderCloudInitHugepages(t *testing.T) {
	t.Parallel()

	inputs := testCloudInitInputs()
	inputs.InstanceType.Capacity[corev1.ResourceHugePagesPrefix+"1Gi"] = resource.MustParse("2Gi")
	inputs.InstanceType.Capacity[corev1.ResourceHugePagesPrefix+"2Mi"] = resource.MustParse("512Mi")
	inputs.Templates = map[string][]byte{"user-data": []byte("{{ .Resources.Hugepages1Gi }} {{ .Resources.Hugepages2Mi }}")}

	files, err := renderCloudInit(inputs)
	require.NoError(t, err)

	assert.Equal(t, "2 256", files.UserData)
}
