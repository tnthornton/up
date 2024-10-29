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
	"fmt"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	xpextv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/workspace"
	"github.com/upbound/up/internal/xpkg/workspace/meta"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

type Cmd struct {
	NewRepository string `arg:"" help:"The new repository for the project."`
	ProjectFile   string `short:"f" help:"Path to the project definition file." default:"upbound.yaml"`

	projFS afero.Fs
	ws     *workspace.Workspace
}

func (c *Cmd) AfterApply(p pterm.TextPrinter) error {
	// The location of the project file defines the root of the project.
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	ws, err := workspace.New("/",
		workspace.WithFS(c.projFS),
		workspace.WithPrinter(p),
		workspace.WithPermissiveParser(),
	)
	if err != nil {
		return err
	}
	c.ws = ws

	if err := ws.Parse(context.Background()); err != nil {
		return err
	}

	return nil
}

func (c *Cmd) Run(ctx context.Context, p pterm.TextPrinter) error {
	// Create a mapping of original function names to new function names so we
	// can replace the function references in compositions.
	fnMap, err := c.buildFunctionMap()
	if err != nil {
		return err
	}

	// Update the repository in the project metadata.
	metaProj, ok := c.ws.View().Meta().Object().(*v1alpha1.Project)
	if !ok {
		return errors.New("project has unexpected metadata type")
	}
	metaProj.Spec.Repository = c.NewRepository
	if err := c.ws.Write(meta.New(metaProj)); err != nil {
		return errors.Wrap(err, "failed to write project metadata")
	}

	// Update embedded function references in compositions.
	if err := c.updateCompositions(fnMap, p); err != nil {
		return err
	}

	return nil
}

func (c *Cmd) buildFunctionMap() (map[string]string, error) {
	projFilePath := filepath.Join("/", filepath.Base(c.ProjectFile))
	proj, err := project.Parse(c.projFS, projFilePath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse project file")
	}

	oldRepo := proj.Spec.Repository
	infos, err := afero.ReadDir(c.projFS, proj.Spec.Paths.Functions)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list functions")
	}
	fnMap := make(map[string]string)
	for _, info := range infos {
		if info.IsDir() {
			oldRepo := fmt.Sprintf("%s_%s", oldRepo, info.Name())
			oldRef, err := name.ParseReference(oldRepo)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse old function repo")
			}
			oldName := xpkg.ToDNSLabel(oldRef.Context().RepositoryStr())
			newRepo := fmt.Sprintf("%s_%s", c.NewRepository, info.Name())
			newRef, err := name.ParseReference(newRepo)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse new function repo")
			}
			newName := xpkg.ToDNSLabel(newRef.Context().RepositoryStr())

			fnMap[oldName] = newName
		}
	}

	return fnMap, nil
}

func (c *Cmd) updateCompositions(fnMap map[string]string, p pterm.TextPrinter) error {
	for _, node := range c.ws.View().Nodes() {
		var comp xpextv1.Composition
		unst := node.GetObject().(*unstructured.Unstructured)
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(unst.UnstructuredContent(), &comp)
		if err != nil {
			continue
		}

		if comp.Spec.Mode == nil || *comp.Spec.Mode != xpextv1.CompositionModePipeline {
			continue
		}

		newPipeline := make([]xpextv1.PipelineStep, len(comp.Spec.Pipeline))
		rewritten := false
		for i, step := range comp.Spec.Pipeline {
			newRef, update := fnMap[step.FunctionRef.Name]
			if update {
				step.FunctionRef.Name = newRef
				rewritten = true
				p.Printfln("Updating step %q in composition %s", step.Step, comp.Name)
			}
			newPipeline[i] = step
		}
		comp.Spec.Pipeline = newPipeline

		if !rewritten {
			continue
		}

		fname := node.GetFileName()
		compYAML, err := yaml.Marshal(comp)
		if err != nil {
			return errors.Wrapf(err, "failed to marshal updated composition %q", comp.Name)
		}
		if err := afero.WriteFile(c.projFS, fname, compYAML, 0644); err != nil {
			return errors.Wrapf(err, "failed to write updated composition %q", comp.Name)
		}

		p.Printfln("Wrote updated composition to %s", fname)
	}

	return nil
}
