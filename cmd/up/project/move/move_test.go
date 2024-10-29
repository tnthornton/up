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

package move

import (
	"context"
	"embed"
	"io/fs"
	"strings"
	"testing"

	xpextv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/xpkg/workspace"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

//go:embed testdata/project-embedded-functions/**
var projectEmbeddedFunctions embed.FS

func TestMove(t *testing.T) {
	// Move updates files in-place, so we use a CoW filesystem on top of the
	// read-only embed filesystem.
	projFS := afero.NewBasePathFs(
		afero.FromIOFS{FS: projectEmbeddedFunctions},
		"testdata/project-embedded-functions",
	)
	projFS = afero.NewCopyOnWriteFs(projFS, afero.NewMemMapFs())

	ws, err := workspace.New("/",
		workspace.WithFS(projFS),
		workspace.WithPrinter(&pterm.DefaultBasicText),
		workspace.WithPermissiveParser(),
	)
	assert.NilError(t, err)
	err = ws.Parse(context.Background())
	assert.NilError(t, err)

	c := &Cmd{
		projFS:        projFS,
		ws:            ws,
		NewRepository: "docker.io/my-org/my-project",
		ProjectFile:   "upbound.yaml",
	}

	err = c.Run(context.Background(), &pterm.DefaultBasicText)
	assert.NilError(t, err)

	// Validate that the repository was updated in the project metadata.
	var updatedProject v1alpha1.Project
	projectBytes, err := afero.ReadFile(projFS, "/upbound.yaml")
	assert.NilError(t, err)
	err = yaml.Unmarshal(projectBytes, &updatedProject)
	assert.NilError(t, err)
	assert.Equal(t, updatedProject.Spec.Repository, c.NewRepository)

	// Validate that function references were updated.
	compositionsUpdated := 0
	err = afero.Walk(projFS, "/apis", func(path string, info fs.FileInfo, err error) error {
		assert.NilError(t, err)
		if info.Name() != "composition.yaml" {
			return nil
		}
		var comp xpextv1.Composition
		bs, err := afero.ReadFile(projFS, path)
		assert.NilError(t, err)
		err = yaml.Unmarshal(bs, &comp)
		assert.NilError(t, err)

		for _, step := range comp.Spec.Pipeline {
			if step.Step == "compose" {
				assert.Assert(t, strings.HasPrefix(step.FunctionRef.Name, "my-org-my-project"))
				compositionsUpdated++
			}
		}

		return nil
	})
	assert.NilError(t, err)
	assert.Equal(t, compositionsUpdated, 3)
}
