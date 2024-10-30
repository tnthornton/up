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
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crossplane/crossplane-runtime/pkg/parser"
	xpv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	xpmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/pkg/errors"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/async"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/functions"
	"github.com/upbound/up/internal/xpkg/mutators"
	"github.com/upbound/up/internal/xpkg/parser/examples"
	"github.com/upbound/up/internal/xpkg/parser/schema"
	pyaml "github.com/upbound/up/internal/xpkg/parser/yaml"
	"github.com/upbound/up/internal/xpkg/schemagenerator"
	"github.com/upbound/up/internal/xpkg/schemarunner"
	"github.com/upbound/up/internal/xpkg/workspace"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

const (
	// ConfigurationTag is the tag used for the configuration image in the built
	// package.
	ConfigurationTag = "configuration"
)

// ImageTagMap is a map of container image tags to images.
type ImageTagMap map[name.Tag]v1.Image

// Builder is able to build a project into a set of packages.
type Builder interface {
	// Build builds a project into a set of packages. It returns a map
	// containing images that were built from the project. The returned map will
	// always include one image with the ConfigurationTag, which is the
	// configuration package built from the APIs found in the project.
	Build(ctx context.Context, project *v1alpha1.Project, projectFS afero.Fs, opts ...BuildOption) (ImageTagMap, error)
}

// BuilderOption configures a builder.
type BuilderOption func(b *realBuilder)

// BuildWithFunctionIdentifier sets the function identifier that will be used to
// find function builders for any functions in a project.
func BuildWithFunctionIdentifier(i functions.Identifier) BuilderOption {
	return func(b *realBuilder) {
		b.functionIdentifier = i
	}
}

// BuildWithSchemaRunner sets the runner that will be used to run containers
// used for schema generation.
func BuildWithSchemaRunner(r schemarunner.SchemaRunner) BuilderOption {
	return func(b *realBuilder) {
		b.schemaRunner = r
	}
}

// BuildWithMaxConcurrency sets the maximum concurrency for building embedded
// functions.
func BuildWithMaxConcurrency(n uint) BuilderOption {
	return func(b *realBuilder) {
		b.maxConcurrency = n
	}
}

// BuildOption configures a build.
type BuildOption func(o *buildOptions)

type buildOptions struct {
	eventChan   async.EventChannel
	imageLabels map[string]string
	depManager  *manager.Manager
}

// BuildWithEventChannel provides a channel to which progress updates will be
// written during the build. It is the caller's responsibility to manage the
// lifecycle of this channel.
func BuildWithEventChannel(ch async.EventChannel) BuildOption {
	return func(o *buildOptions) {
		o.eventChan = ch
	}
}

// BuildWithImageLabels provides labels that will be added to all images after
// they are built.
func BuildWithImageLabels(labels map[string]string) BuildOption {
	return func(o *buildOptions) {
		o.imageLabels = labels
	}
}

// BuildWithDependencyManager provides a dependency manager to use for
// dependency resolution during build.
func BuildWithDependencyManager(m *manager.Manager) BuildOption {
	return func(o *buildOptions) {
		o.depManager = m
	}
}

type realBuilder struct {
	functionIdentifier functions.Identifier
	schemaRunner       schemarunner.SchemaRunner
	maxConcurrency     uint
}

// Build implements the Builder interface.
func (b *realBuilder) Build(ctx context.Context, project *v1alpha1.Project, projectFS afero.Fs, opts ...BuildOption) (ImageTagMap, error) { //nolint:gocyclo
	os := &buildOptions{}
	for _, opt := range opts {
		opt(os)
	}

	// Check that we have all the dependencies in the cache for function
	// building.
	statusStage := "Checking dependencies"
	os.eventChan.SendEvent(statusStage, async.EventStatusStarted)
	if err := b.checkDependencies(ctx, projectFS, os.depManager); err != nil {
		os.eventChan.SendEvent(statusStage, async.EventStatusFailure)
		return nil, err
	}
	os.eventChan.SendEvent(statusStage, async.EventStatusSuccess)

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

	functionsSource := afero.NewBasePathFs(projectFS, project.Spec.Paths.Functions)
	// By default we search the whole project directory except the examples
	// directory.
	apisSource := projectFS
	apiExcludes := []string{project.Spec.Paths.Examples, project.Spec.Paths.Functions}
	if project.Spec.Paths.APIs != "/" {
		apisSource = afero.NewBasePathFs(projectFS, project.Spec.Paths.APIs)
		apiExcludes = []string{}
	}

	// In parallel:
	// * Collect APIs (composites).
	// * Generate schemas for APIs.
	eg, ctx := errgroup.WithContext(ctx)

	// Collect APIs (composites).
	var packageFS afero.Fs
	eg.Go(func() error {
		pfs, err := collectComposites(apisSource, apiExcludes)
		packageFS = pfs
		return err
	})

	// Generate KCL Schemas
	var (
		mut   []xpkg.Mutator
		mutMu sync.Mutex
	)
	eg.Go(func() error {
		statusStage := "Generating KCL schemas"
		os.eventChan.SendEvent(statusStage, async.EventStatusStarted)
		kfs, err := schemagenerator.GenerateSchemaKcl(ctx, apisSource, apiExcludes, b.schemaRunner)
		if err != nil {
			os.eventChan.SendEvent(statusStage, async.EventStatusFailure)
			return err
		}

		if kfs != nil {
			mutMu.Lock()
			mut = append(mut, mutators.NewSchemaMutator(schema.New(kfs, "", xpkg.StreamFileMode), xpkg.SchemaKclAnnotation))
			mutMu.Unlock()

			if os.depManager != nil {
				if err := os.depManager.AddModels("kcl", kfs); err != nil {
					return err
				}
			}
		}

		os.eventChan.SendEvent(statusStage, async.EventStatusSuccess)
		return nil
	})

	// Generate Python Schemas
	eg.Go(func() error {
		statusStage := "Generating Python schemas"
		os.eventChan.SendEvent(statusStage, async.EventStatusStarted)
		pfs, err := schemagenerator.GenerateSchemaPython(ctx, apisSource, apiExcludes, b.schemaRunner)
		if err != nil {
			os.eventChan.SendEvent(statusStage, async.EventStatusFailure)
			return err
		}

		if pfs != nil {
			mutMu.Lock()
			mut = append(mut, mutators.NewSchemaMutator(schema.New(pfs, "", xpkg.StreamFileMode), xpkg.SchemaPythonAnnotation))
			mutMu.Unlock()
			if os.depManager != nil {
				if err := os.depManager.AddModels("python", pfs); err != nil {
					return err
				}
			}
		}

		os.eventChan.SendEvent(statusStage, async.EventStatusSuccess)
		return nil
	})

	err := eg.Wait()
	if err != nil {
		return nil, err
	}

	// Find and build embedded functions. This has to come after schema
	// generation because functions may depend on the generated schemas.
	statusStage = "Building functions"
	os.eventChan.SendEvent(statusStage, async.EventStatusStarted)
	imgMap, deps, err := b.buildFunctions(ctx, functionsSource, project)
	if err != nil {
		os.eventChan.SendEvent(statusStage, async.EventStatusFailure)
		return nil, err
	}
	// Add embedded function dependencies to the configuration.
	cfg.Spec.DependsOn = append(cfg.Spec.DependsOn, deps...)
	os.eventChan.SendEvent(statusStage, async.EventStatusSuccess)

	// Add the package metadata to the collected composites.
	statusStage = "Building configuration package"
	os.eventChan.SendEvent(statusStage, async.EventStatusStarted)
	defer func() {
		if err != nil {
			os.eventChan.SendEvent(statusStage, async.EventStatusFailure)
		} else {
			os.eventChan.SendEvent(statusStage, async.EventStatusSuccess)
		}
	}()

	y, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal package metadata")
	}
	err = afero.WriteFile(packageFS, "/crossplane.yaml", y, 0644)
	if err != nil {
		return nil, errors.Wrap(err, "failed to write package metadata")
	}

	// Build the configuration package from the constructed filesystem.
	pp, err := pyaml.New()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create parser")
	}
	builder := xpkg.New(
		parser.NewFsBackend(packageFS, parser.FsDir("/")),
		nil,
		parser.NewFsBackend(afero.NewBasePathFs(projectFS, project.Spec.Paths.Examples),
			parser.FsDir("/"),
			parser.FsFilters(parser.SkipNotYAML()),
		),
		pp,
		examples.New(),
		mut...,
	)

	img, _, err := builder.Build(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build package")
	}

	if os.imageLabels != nil {
		img, err = addLabels(img, os.imageLabels)
		if err != nil {
			return nil, errors.Wrapf(err, "failed add labels to package")
		}
	}

	// Write out the packages to a file, which can be consumed by up project
	// push.
	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, ConfigurationTag))
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct image tag")
	}
	imgMap[imgTag] = img

	return imgMap, nil
}

func (b *realBuilder) checkDependencies(ctx context.Context, projectFS afero.Fs, m *manager.Manager) error {
	ws, err := workspace.New("/",
		workspace.WithFS(projectFS),
		// The user doesn't care about workspace warnings during build.
		workspace.WithPrinter(&pterm.BasicTextPrinter{Writer: io.Discard}),
		workspace.WithPermissiveParser(),
	)
	if err != nil {
		return errors.Wrap(err, "failed to create workspace")
	}
	if err := ws.Parse(ctx); err != nil {
		return errors.Wrap(err, "failed to parse workspace")
	}
	deps, err := ws.View().Meta().DependsOn()
	if err != nil {
		return errors.Wrap(err, "failed to get dependencies")
	}
	for _, dep := range deps {
		_, _, err := m.AddAll(ctx, dep)
		if err != nil {
			return errors.Wrapf(err, "failed to check dependency %q", dep.Package)
		}
	}

	return nil
}

// buildFunctions builds the embedded functions found in directories at the top
// level of the provided filesystem. The resulting images are returned in a map
// where the keys are their tags, suitable for writing to a file with
// go-containerregistry's `tarball.MultiWrite`.
func (b *realBuilder) buildFunctions(ctx context.Context, fromFS afero.Fs, project *v1alpha1.Project) (ImageTagMap, []xpmetav1.Dependency, error) { //nolint:gocyclo // This is fine.
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

	// Semaphore to limit the number of functions we build in parallel.
	sem := make(chan struct{}, b.maxConcurrency)
	for i, fnName := range fnDirs {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			fnRepo := fmt.Sprintf("%s_%s", project.Spec.Repository, fnName)
			fnFS := afero.NewBasePathFs(fromFS, fnName)
			imgs, err := b.buildFunction(ctx, fnFS, project, fnName)
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

// buildFunction builds images for a single function whose source resides in the
// given filesystem. One image will be returned for each architecture specified
// in the project.
func (b *realBuilder) buildFunction(ctx context.Context, fromFS afero.Fs, project *v1alpha1.Project, fnName string) ([]v1.Image, error) { //nolint:gocyclo // Factoring anything out here would be unnatural.
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

	fnBuilder, err := b.functionIdentifier.Identify(fromFS)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find a builder")
	}

	// Resolve the real absolute path to the function directory if
	// possible. This is required for following symlinks in the function
	// directory.
	var osBasePath string
	if bfs, ok := fromFS.(*afero.BasePathFs); ok {
		osBasePath = afero.FullBaseFsPath(bfs, ".")
	}

	runtimeImages, err := fnBuilder.Build(ctx, fromFS, project.Spec.Architectures, osBasePath)
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

func addLabels(img v1.Image, labels map[string]string) (v1.Image, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "error getting config file")
	}

	if cfgFile.Config.Labels == nil {
		cfgFile.Config.Labels = make(map[string]string)
	}

	for k, v := range labels {
		cfgFile.Config.Labels[k] = v
	}

	// Mutate the image to include the updated configuration with labels
	updatedImg, err := mutate.ConfigFile(img, cfgFile)
	if err != nil {
		return nil, errors.Wrap(err, "error updating config file with labels")
	}

	return updatedImg, nil
}

// NewBuilder returns a new project builder.
func NewBuilder(opts ...BuilderOption) *realBuilder {
	b := &realBuilder{
		functionIdentifier: functions.DefaultIdentifier,
		schemaRunner:       schemarunner.RealSchemaRunner{},
		maxConcurrency:     8,
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}
