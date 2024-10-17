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

package project

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name          string
		setupFs       func(fs afero.Fs)
		projectFile   string
		expectErr     bool
		expectedPaths *v1alpha1.ProjectPaths
	}{
		{
			name: "ValidProjectFileAllPaths",
			setupFs: func(fs afero.Fs) {
				yamlContent := `
apiVersion: v1alpha1
kind: Project
metadata:
  name: ValidProjectFileAllPaths
spec:
  repository: xpkg.upbound.io/upbound/getting-started
  paths:
    apis: "apis"
    examples: "example"
    functions: "funcs"
`
				afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm)
			},
			projectFile: "/project.yaml",
			expectErr:   false,
			expectedPaths: &v1alpha1.ProjectPaths{
				APIs:      "/apis",
				Examples:  "/example",
				Functions: "/funcs",
			},
		},
		{
			name: "ValidProjectFileSomePaths",
			setupFs: func(fs afero.Fs) {
				yamlContent := `
apiVersion: v1alpha1
kind: Project
metadata:
  name: ValidProjectFileSomePaths
spec:
  repository: xpkg.upbound.io/upbound/getting-started
  paths:
    functions: "funcs"
`
				afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm)
			},
			projectFile: "/project.yaml",
			expectErr:   false,
			expectedPaths: &v1alpha1.ProjectPaths{
				APIs:      "/",
				Examples:  "/examples",
				Functions: "/funcs",
			},
		},
		{
			name: "InvalidProjectFileYAML",
			setupFs: func(fs afero.Fs) {
				afero.WriteFile(fs, "/project.yaml", []byte("invalid yaml content"), os.ModePerm)
			},
			projectFile:   "/project.yaml",
			expectErr:     true,
			expectedPaths: nil,
		},
		{
			name: "ProjectFileWithNoPaths",
			setupFs: func(fs afero.Fs) {
				yamlContent := `
apiVersion: v1alpha1
kind: Project
metadata:
  name: ProjectFileWithNoPaths
spec:
  repository: xpkg.upbound.io/upbound/getting-started
`
				afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm)
			},
			projectFile: "/project.yaml",
			expectErr:   false,
			expectedPaths: &v1alpha1.ProjectPaths{
				APIs:      "/",
				Examples:  "/examples",
				Functions: "/functions",
			},
		},
		{
			name: "ProjectFileNotFound",
			setupFs: func(fs afero.Fs) {
			},
			projectFile:   "/nonexistent.yaml",
			expectErr:     true,
			expectedPaths: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()

			tt.setupFs(fs)

			proj, err := Parse(fs, tt.projectFile)

			if tt.expectErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			require.Equal(t, tt.expectedPaths, proj.Spec.Paths, "incorrect paths for project")
		})
	}
}
