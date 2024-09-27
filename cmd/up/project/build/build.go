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

package build

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/parser"
	xpv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	xpmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/upterm"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/functions"
	"github.com/upbound/up/internal/xpkg/mutators"
	"github.com/upbound/up/internal/xpkg/parser/examples"
	"github.com/upbound/up/internal/xpkg/parser/schema"
	pyaml "github.com/upbound/up/internal/xpkg/parser/yaml"
	"github.com/upbound/up/internal/xpkg/schemagenerator"
	"github.com/upbound/up/internal/xpkg/schemarunner"

	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

const (
	// ConfigurationTag is the tag used for the configuration image in the built
	// package.
	ConfigurationTag = "configuration"
)

type Cmd struct {
	ProjectFile    string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Repository     string `optional:"" help:"Repository for the built package. Overrides the repository specified in the project file."`
	OutputDir      string `short:"o" help:"Path to the output directory, where packages will be written." default:"_output"`
	NoBuildCache   bool   `help:"Don't cache image layers while building." default:"false"`
	BuildCacheDir  string `help:"Path to the build cache directory." type:"path" default:"~/.up/build-cache"`
	MaxConcurrency uint   `help:"Maximum number of functions to build at once." env:"UP_MAX_CONCURRENCY" default:"8"`

	projFS             afero.Fs
	outputFS           afero.Fs
	functionIdentifier functions.Identifier
	schemaRunner       schemarunner.SchemaRunner
}

func (c *Cmd) AfterApply() error {
	// Read the project file.
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	// The location of the project file defines the root of the project.
	projDirPath := filepath.Dir(projFilePath)
	// Construct a virtual filesystem that contains only the project. We'll do
	// all our operations inside this virtual FS.
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	// Output can be anywhere, doesn't have to be in the project directory.
	c.outputFS = afero.NewOsFs()

	c.functionIdentifier = functions.DefaultIdentifier
	c.schemaRunner = schemarunner.RealSchemaRunner{}

	return nil
}

func (c *Cmd) Run(ctx context.Context, p pterm.TextPrinter) error { //nolint:gocyclo // This is fine.
	var mut []xpkg.Mutator
	pterm.EnableStyling()

	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 1
	}

	project, paths, err := c.parseProject()
	if err != nil {
		return err
	}

	// Scaffold a configuration based on the metadata in the project. Later
	// we'll add any embedded functions we build to the dependencies.
	cfg := &xpmetav1.Configuration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: xpmetav1.SchemeGroupVersion.String(),
			Kind:       xpmetav1.ConfigurationKind,
		},
		ObjectMeta: cfgMetaFromProject(project),
		Spec: xpmetav1.ConfigurationSpec{
			MetaSpec: xpmetav1.MetaSpec{
				Crossplane: project.Spec.Crossplane,
				DependsOn:  project.Spec.DependsOn,
			},
		},
	}

	functionsSource := afero.NewBasePathFs(c.projFS, paths.Functions)
	// By default we search the whole project directory except the examples
	// directory.
	apisSource := c.projFS
	apiExcludes := []string{paths.Examples}
	if paths.APIs != "/" {
		apisSource = afero.NewBasePathFs(c.projFS, paths.APIs)
		apiExcludes = []string{}
	}

	// In parallel:
	// * Find embedded functions and build their packages.
	// * Collect APIs (composites).
	var imgMap imageTagMap
	eg, ctx := errgroup.WithContext(ctx)
	// Semaphore to limit the number of functions we build in parallel.
	sem := make(chan struct{}, c.MaxConcurrency)
	eg.Go(func() error {
		sem <- struct{}{}
		defer func() {
			<-sem
		}()

		imgs, deps, err := c.buildFunctions(ctx, functionsSource, project)
		if err != nil {
			return err
		}

		imgMap = imgs
		cfg.Spec.DependsOn = append(cfg.Spec.DependsOn, deps...)

		return nil
	})

	// Collect APIs (composites).
	var packageFS afero.Fs
	eg.Go(func() error {
		pfs, err := collectComposites(apisSource, apiExcludes)
		packageFS = pfs
		return err
	})

	// Generate KCL Schemas
	eg.Go(func() error {
		kfs, err := schemagenerator.GenerateSchemaKcl(ctx, apisSource, apiExcludes, c.schemaRunner)
		if err != nil {
			return err
		}

		if kfs != nil {
			mut = append(mut, mutators.NewSchemaMutator(schema.New(kfs, "", xpkg.StreamFileMode), xpkg.SchemaKclAnnotation))
		}
		return nil
	})

	// Generate Python Schemas
	eg.Go(func() error {
		pfs, err := schemagenerator.GenerateSchemaPython(ctx, apisSource, apiExcludes, c.schemaRunner)
		if err != nil {
			return err
		}

		if pfs != nil {
			mut = append(mut, mutators.NewSchemaMutator(schema.New(pfs, "", xpkg.StreamFileMode), xpkg.SchemaPythonAnnotation))
		}

		return nil
	})

	// wait for go runtines
	err = upterm.WrapWithSuccessSpinner("Building functions", upterm.CheckmarkSuccessSpinner, eg.Wait)
	if err != nil {
		return err
	}

	// Add the package metadata to the collected composites.
	y, err := yaml.Marshal(cfg)
	if err != nil {
		return errors.Wrap(err, "failed to marshal package metadata")
	}
	err = afero.WriteFile(packageFS, "/crossplane.yaml", y, 0644)
	if err != nil {
		return errors.Wrap(err, "failed to write package metadata")
	}

	// Build the configuration package from the constructed filesystem.
	pp, err := pyaml.New()
	if err != nil {
		return errors.Wrap(err, "failed to create parser")
	}
	builder := xpkg.New(
		parser.NewFsBackend(packageFS, parser.FsDir("/")),
		nil,
		parser.NewFsBackend(afero.NewBasePathFs(c.projFS, paths.Examples),
			parser.FsDir("/"),
			parser.FsFilters(parser.SkipNotYAML()),
		),
		pp,
		examples.New(),
		mut...,
	)

	var img v1.Image
	err = upterm.WrapWithSuccessSpinner(
		"Building configuration package",
		upterm.CheckmarkSuccessSpinner,
		func() error {
			img, _, err = builder.Build(ctx)
			if err != nil {
				return errors.Wrap(err, "failed to build package")
			}
			return nil
		})
	if err != nil {
		return err
	}

	// Write out the packages to a file, which can be consumed by up project
	// push.
	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, ConfigurationTag))
	if err != nil {
		return errors.Wrap(err, "failed to construct image tag")
	}
	imgMap[imgTag] = img

	outFile := filepath.Join(c.OutputDir, fmt.Sprintf("%s.uppkg", project.Name))

	err = c.outputFS.MkdirAll(c.OutputDir, 0755)
	if err != nil {
		return errors.Wrapf(err, "failed to create output directory %q", c.OutputDir)
	}

	if !c.NoBuildCache {
		// Create a layer cache so that if we're building on top of base images we
		// only pull their layers once. Note we do this here rather than in the
		// builder because pulling layers is deferred to where we use them, which is
		// here.
		cch := cache.NewFilesystemCache(c.BuildCacheDir)
		for tag, img := range imgMap {
			imgMap[tag] = cache.Image(img, cch)
		}
	}

	err = upterm.WrapWithSuccessSpinner(
		fmt.Sprintf("Writing packages to %s", outFile),
		upterm.CheckmarkSuccessSpinner,
		func() error {
			f, err := c.outputFS.Create(outFile)
			if err != nil {
				return errors.Wrapf(err, "failed to create output file %q", outFile)
			}
			defer f.Close() //nolint:errcheck // Can't do anything useful with this error.

			err = tarball.MultiWrite(imgMap, f)
			if err != nil {
				return errors.Wrap(err, "failed to write package to file")
			}
			return nil
		})
	if err != nil {
		return err
	}

	return nil
}

// parseProject parses the project file, returning the parsed Project resource
// and the absolute paths to various parts of the project in the project
// filesystem.
func (c *Cmd) parseProject() (*v1alpha1.Project, *v1alpha1.ProjectPaths, error) {
	// Parse and validate the project file.
	projYAML, err := afero.ReadFile(c.projFS, filepath.Join("/", filepath.Base(c.ProjectFile)))
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to read project file %q", c.ProjectFile)
	}
	var project v1alpha1.Project
	err = yaml.Unmarshal(projYAML, &project)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to parse project file")
	}
	if err := project.Validate(); err != nil {
		return nil, nil, errors.Wrap(err, "invalid project file")
	}

	if c.Repository != "" {
		project.Spec.Repository = c.Repository
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

type imageTagMap map[name.Tag]v1.Image

// buildFunctions builds the embedded functions found in directories at the top
// level of the provided filesystem. The resulting images are returned in a map
// where the keys are their tags, suitable for writing to a file with
// go-containerregistry's `tarball.MultiWrite`.
func (c *Cmd) buildFunctions(ctx context.Context, fromFS afero.Fs, project *v1alpha1.Project) (imageTagMap, []xpmetav1.Dependency, error) { //nolint:gocyclo // This is fine.
	var (
		imgMap = make(map[name.Tag]v1.Image)
		imgMu  sync.Mutex
	)

	infos, err := afero.ReadDir(fromFS, "/")
	switch {
	case os.IsNotExist(err):
		// There are no functions.
		return imgMap, nil, nil
	case err != nil:
		return nil, nil, errors.Wrap(err, "failed to list functions directory")
	}

	fnDirs := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.IsDir() {
			fnDirs = append(fnDirs, info.Name())
		}
	}

	deps := make([]xpmetav1.Dependency, len(fnDirs))
	eg, ctx := errgroup.WithContext(ctx)

	for i, fnName := range fnDirs {
		eg.Go(func() error {
			fnRepo := fmt.Sprintf("%s_%s", project.Spec.Repository, fnName)
			fnFS := afero.NewBasePathFs(fromFS, fnName)
			imgs, err := c.buildFunction(ctx, fnFS, project, fnName)
			if err != nil {
				return errors.Wrapf(err, "failed to build function %q", fnName)
			}

			// Construct an index so we know the digest for the dependency. This
			// index will be reproduced when we push the image.
			idx, imgs, err := xpkg.BuildIndex(imgs...)
			if err != nil {
				return errors.Wrapf(err, "failed to construct index for function image %q", fnName)
			}
			dgst, err := idx.Digest()
			if err != nil {
				return errors.Wrapf(err, "failed to get index digest for function image %q", fnName)
			}
			deps[i] = xpmetav1.Dependency{
				Function: &fnRepo,
				Version:  dgst.String(),
			}

			for _, img := range imgs {
				cfg, err := img.ConfigFile()
				if err != nil {
					return errors.Wrapf(err, "failed to get config for function image %q", fnName)
				}

				tag := fmt.Sprintf("%s:%s", fnRepo, cfg.Architecture)
				imgTag, err := name.NewTag(tag)
				if err != nil {
					return errors.Wrapf(err, "failed to construct tag for function image %q", fnName)
				}
				imgMu.Lock()
				imgMap[imgTag] = img
				imgMu.Unlock()
			}

			return nil
		})
	}

	err = eg.Wait()
	if err != nil {
		return nil, nil, err
	}

	return imgMap, deps, nil
}

// buildFunction builds iamges for a single function whose source resides in the
// given filesystem. One image will be returned for each architecture specified
// in the project.
func (c *Cmd) buildFunction(ctx context.Context, fromFS afero.Fs, project *v1alpha1.Project, fnName string) ([]v1.Image, error) {
	fn := &xpmetav1.Function{
		TypeMeta: metav1.TypeMeta{
			APIVersion: xpmetav1.SchemeGroupVersion.String(),
			Kind:       xpmetav1.FunctionKind,
		},
		ObjectMeta: fnMetaFromProject(project, fnName),
	}
	metaFS := afero.NewMemMapFs()
	y, err := yaml.Marshal(fn)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal function metadata")
	}
	err = afero.WriteFile(metaFS, "/crossplane.yaml", y, 0644)
	if err != nil {
		return nil, errors.Wrap(err, "failed to write function metadata")
	}

	// Note there's no way to configure the location of examples in an embedded
	// function. If we start supporting projects as embedded functions we should
	// probably change this, but for now this is good enough.
	examplesParser := parser.NewEchoBackend("")
	examplesExist, err := afero.IsDir(fromFS, "/examples")
	switch {
	case err == nil, os.IsNotExist(err):
		// Check examplesExist to determine whether to parse examples.
	default:
		return nil, errors.Wrap(err, "failed to check for examples")
	}
	if examplesExist {
		examplesParser = parser.NewFsBackend(fromFS,
			parser.FsDir("/examples"),
			parser.FsFilters(parser.SkipNotYAML()),
		)
	}

	pp, err := pyaml.New()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create parser")
	}
	builder := xpkg.New(
		parser.NewFsBackend(metaFS, parser.FsDir("/")),
		nil,
		examplesParser,
		pp,
		examples.New(),
	)

	fnBuilder, err := c.functionIdentifier.Identify(fromFS)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find a builder")
	}

	runtimeImages, err := fnBuilder.Build(ctx, fromFS, project.Spec.Architectures)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build runtime images")
	}

	pkgImages := make([]v1.Image, 0, len(runtimeImages))

	for _, img := range runtimeImages {
		pkgImage, _, err := builder.Build(ctx, xpkg.WithController(img))
		if err != nil {
			return nil, errors.Wrap(err, "failed to build function package")
		}
		pkgImages = append(pkgImages, pkgImage)
	}

	return pkgImages, nil
}

func collectComposites(fromFS afero.Fs, exclude []string) (afero.Fs, error) { //nolint:gocyclo // This is fine.
	toFS := afero.NewMemMapFs()
	return toFS, afero.Walk(fromFS, "/", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		for _, excl := range exclude {
			if strings.HasPrefix(path, excl) {
				return filepath.SkipDir
			}
		}

		if info.IsDir() {
			return nil
		}
		// Ignore files without yaml extensions.
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		var u metav1.TypeMeta
		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}
		err = yaml.Unmarshal(bs, &u)
		if err != nil {
			return errors.Wrapf(err, "failed to parse file %q", path)
		}

		// Ignore anything that's not an XRD or Composition, since those are the
		// only allowed types in a Configuration xpkg.
		if u.GroupVersionKind().Group != xpv1.Group {
			return nil
		}
		if u.Kind != xpv1.CompositeResourceDefinitionKind && u.Kind != xpv1.CompositionKind {
			return nil
		}

		// Copy the file into the package FS.
		err = afero.WriteFile(toFS, path, bs, 0644)
		if err != nil {
			return errors.Wrapf(err, "failed to write file %q to package", path)
		}

		return nil
	})
}

func cfgMetaFromProject(proj *v1alpha1.Project) metav1.ObjectMeta {
	meta := proj.ObjectMeta.DeepCopy()

	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}

	meta.Annotations["meta.crossplane.io/maintainer"] = proj.Spec.Maintainer
	meta.Annotations["meta.crossplane.io/source"] = proj.Spec.Source
	meta.Annotations["meta.crossplane.io/license"] = proj.Spec.License
	meta.Annotations["meta.crossplane.io/description"] = proj.Spec.Description
	meta.Annotations["meta.crossplane.io/readme"] = proj.Spec.Readme

	return *meta
}

func fnMetaFromProject(proj *v1alpha1.Project, fnName string) metav1.ObjectMeta {
	meta := proj.ObjectMeta.DeepCopy()

	meta.Name = fmt.Sprintf("%s-%s", meta.Name, fnName)

	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}

	meta.Annotations["meta.crossplane.io/maintainer"] = proj.Spec.Maintainer
	meta.Annotations["meta.crossplane.io/source"] = proj.Spec.Source
	meta.Annotations["meta.crossplane.io/license"] = proj.Spec.License
	meta.Annotations["meta.crossplane.io/description"] = fmt.Sprintf("Function %s from project %s", fnName, proj.Name)

	return *meta
}
