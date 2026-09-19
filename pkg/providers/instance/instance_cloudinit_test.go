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

	isoPath, cleanup, err := buildCloudInitISO("build-cloud-init-iso-test.iso",
		"user-data-content", "meta-data-content", "vendor-data-content", "network-config-content")
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

	isoPath, cleanup, err := buildCloudInitISO("build-cloud-init-iso-test-minimal.iso",
		"user-data-content", "meta-data-content", "", "")
	defer cleanup()

	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"user-data": "user-data-content",
		"meta-data": "meta-data-content",
	}, readISOFile(t, isoPath))
}
