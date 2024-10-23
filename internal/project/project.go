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
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

// Parse parses the project file, returning the parsed Project resource
// and the absolute paths to various parts of the project in the project
// filesystem.
func Parse(projFS afero.Fs, projectFile, repository string) (*v1alpha1.Project, *v1alpha1.ProjectPaths, error) {
	// Parse and validate the project file.
	projYAML, err := afero.ReadFile(projFS, filepath.Join("/", filepath.Base(projectFile)))
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to read project file %q", projectFile)
	}
	var project v1alpha1.Project
	err = yaml.Unmarshal(projYAML, &project)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to parse project file")
	}
	if repository != "" {
		project.Spec.Repository = repository
	}
	if err := project.Validate(); err != nil {
		return nil, nil, errors.Wrap(err, "invalid project file")
	}
	// Construct absolute versions of the other configured paths for use within
	// the virtual FS.
	paths := &v1alpha1.ProjectPaths{
		APIs:      "/",
		Examples:  "/examples",
		Functions: "/functions",
	}
	if project.Spec.Paths != nil {
		if project.Spec.Paths.APIs != "" {
			paths.APIs = filepath.Clean(filepath.Join("/", project.Spec.Paths.APIs))
		}
		if project.Spec.Paths.Examples != "" {
			paths.Examples = filepath.Clean(filepath.Join("/", project.Spec.Paths.Examples))
		}
		if project.Spec.Paths.Functions != "" {
			paths.Functions = filepath.Clean(filepath.Join("/", project.Spec.Paths.Functions))
		}
	}

	return &project, paths, nil
}
