// Copyright 2024 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package schemagenerator

import (
	_ "embed"
	"os"
	"testing"

	"github.com/spf13/afero"
)

// TestTransformStructureKcl tests reorganizing files and adjusting imports.
func TestTransformStructureKcl(t *testing.T) {

	// Test case structure
	tests := []struct {
		name           string
		setupFs        func(fs afero.Fs) // Setup for the filesystem
		sourceDir      string
		targetDir      string
		expectedFiles  map[string]string // expected file paths and their content
		expectedErrors bool
	}{
		{
			name: "TransformStructureKcl",
			setupFs: func(fs afero.Fs) {
				afero.WriteFile(fs, kclModelsFolder+"/kcl.mod", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/kcl.mod.lock", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/k8s/apimachinery/pkg/apis/meta/v1/object_meta.k", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/k8s/apimachinery/pkg/apis/meta/v1/owner_reference.k", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/v1beta1/rds_aws_upbound_io_v1beta1_cluster_activity_stream.k", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/v1beta2/rds_aws_upbound_io_v1beta2_cluster.k", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/v1beta1/rds_aws_upbound_io_v1beta1_subnet_group.k", []byte(""), os.ModePerm)
				afero.WriteFile(fs, kclModelsFolder+"/v1beta1/redshift_aws_upbound_io_v1beta1_subnet_group.k", []byte(""), os.ModePerm)
			},

			sourceDir: kclModelsFolder,
			targetDir: kclAdoptModelsStructure,
			expectedFiles: map[string]string{
				kclAdoptModelsStructure + "/kcl.mod":                                                  "",
				kclAdoptModelsStructure + "/kcl.mod.lock":                                             "",
				kclAdoptModelsStructure + "/k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k": "",
				kclAdoptModelsStructure + "/k8s/apimachinery/pkg/apis/meta/v1/object_meta.k":          "",
				kclAdoptModelsStructure + "/k8s/apimachinery/pkg/apis/meta/v1/owner_reference.k":      "",
				kclAdoptModelsStructure + "/io/upbound/aws/rds/v1beta1/clusteractivitystream.k":       "",
				kclAdoptModelsStructure + "/io/upbound/aws/rds/v1beta2/cluster.k":                     "",
				kclAdoptModelsStructure + "/io/upbound/aws/rds/v1beta1/subnetgroup.k":                 "",
				kclAdoptModelsStructure + "/io/upbound/aws/redshift/v1beta1/subnetgroup.k":            "",
			},
			expectedErrors: false,
		},
	}

	// Iterate over test cases
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs() // Create an in-memory filesystem
			tt.setupFs(fs)            // Set up the initial filesystem structure

			// Run the transformation function
			err := transformStructureKcl(fs, tt.sourceDir, tt.targetDir)

			// Check if errors match expectations
			if tt.expectedErrors && err == nil {
				t.Fatalf("Expected an error but got none")
			} else if !tt.expectedErrors && err != nil {
				t.Fatalf("Did not expect an error, but got: %v", err)
			}

			// Validate the resulting file structure
			for expectedFile := range tt.expectedFiles {
				_, err := afero.ReadFile(fs, expectedFile)
				if err != nil {
					t.Fatalf("Expected file %s does not exist: %v", expectedFile, err)
				}
			}
		})
	}
}
