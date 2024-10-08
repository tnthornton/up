// Copyright 2024 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package crd

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/template.fn.crossplane.io_kclinputs.yaml
var testCRD []byte

func TestConvertToOpenAPI(t *testing.T) {
	tests := []struct {
		name        string
		crdContent  []byte
		expectedErr bool
		validate    func(t *testing.T, fs afero.Fs, outputPath string)
	}{
		{
			name:        "ValidCRDFromEmbed",
			crdContent:  testCRD, // using the embedded CRD file
			expectedErr: false,
			validate: func(t *testing.T, fs afero.Fs, outputPath string) {
				// Check if the file exists
				require.True(t, strings.HasSuffix(outputPath, "template_fn_crossplane_io_v1beta1_kclinput.yaml"))

				// Read the content from the file in-memory
				openAPIContent, err := afero.ReadFile(fs, outputPath)
				require.NoError(t, err)

				// Validate some content in the generated OpenAPI schema
				require.Contains(t, string(openAPIContent), "components")
				require.Contains(t, string(openAPIContent), "schemas")
				require.Contains(t, string(openAPIContent), "io.crossplane.fn.template.v1beta1.KCLInput")
			},
		},
		{
			name: "InvalidCRD",
			crdContent: []byte(`
invalid: crd content
`),
			expectedErr: true,
			validate: func(t *testing.T, fs afero.Fs, outputPath string) {
				// No validation needed because an error is expected
			},
		},
		{
			name: "CRDMissingVersion",
			crdContent: []byte(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: testresources.testgroup.example.com
spec:
  group: testgroup.example.com
  versions: []
  names:
    kind: TestResource
    plural: testresources
`),
			expectedErr: true,
			validate: func(t *testing.T, fs afero.Fs, outputPath string) {
				// No validation needed because an error is expected
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use an in-memory filesystem
			fs := afero.NewMemMapFs()

			// Call ConvertToOpenAPI
			outputPath, err := ConvertToOpenAPI(fs, tt.crdContent, "test-crd.yaml", "base-folder")

			// Check if an error was expected
			if tt.expectedErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Call the validation function if provided
			tt.validate(t, fs, outputPath)
		})
	}
}
