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
	"embed"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	xpmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/xpkg"
	xpkgmarshaler "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
	"github.com/upbound/up/internal/xpkg/functions"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

//go:embed testdata/configuration-getting-started/**
var configurationGettingStarted embed.FS

//go:embed testdata/project-embedded-functions/**
var projectEmbeddedFunctions embed.FS

func TestBuild(t *testing.T) {
	tcs := map[string]struct {
		projFS                  afero.Fs
		outputFile              string
		expectedFunctions       []*xpmetav1.Function
		expectedAnnotatedLayers map[string]bool
		expectedObjectCount     int
	}{
		"ConfigurationOnly": {
			projFS: afero.NewBasePathFs(
				afero.FromIOFS{FS: configurationGettingStarted},
				"testdata/configuration-getting-started",
			),
			outputFile:        "_output/configuration-getting-started.uppkg",
			expectedFunctions: nil,
			// 8 APIs = 8 XRDs + 8 compositions.
			expectedObjectCount: 16,
			expectedAnnotatedLayers: map[string]bool{
				xpkg.SchemaKclAnnotation:    true,
				xpkg.SchemaPythonAnnotation: true,
				xpkg.PackageAnnotation:      true,
				xpkg.ExamplesAnnotation:     true,
			},
		},
		"EmbeddedFunctions": {
			projFS: afero.NewBasePathFs(
				afero.FromIOFS{FS: projectEmbeddedFunctions},
				"testdata/project-embedded-functions",
			),
			outputFile: "_output/project-embedded-functions.uppkg",
			expectedFunctions: []*xpmetav1.Function{
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: xpmetav1.SchemeGroupVersion.String(),
						Kind:       xpmetav1.FunctionKind,
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "project-embedded-functions-xcluster",
						Annotations: map[string]string{
							"meta.crossplane.io/maintainer":  "Upbound <support@upbound.io>",
							"meta.crossplane.io/source":      "github.com/upbound/project-getting-started",
							"meta.crossplane.io/license":     "Apache-2.0",
							"meta.crossplane.io/description": "Function xcluster from project project-embedded-functions",
						},
					},
				},
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: xpmetav1.SchemeGroupVersion.String(),
						Kind:       xpmetav1.FunctionKind,
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "project-embedded-functions-xnetwork",
						Annotations: map[string]string{
							"meta.crossplane.io/maintainer":  "Upbound <support@upbound.io>",
							"meta.crossplane.io/source":      "github.com/upbound/project-getting-started",
							"meta.crossplane.io/license":     "Apache-2.0",
							"meta.crossplane.io/description": "Function xnetwork from project project-embedded-functions",
						},
					},
				},
				{
					TypeMeta: metav1.TypeMeta{
						APIVersion: xpmetav1.SchemeGroupVersion.String(),
						Kind:       xpmetav1.FunctionKind,
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "project-embedded-functions-xsubnetwork",
						Annotations: map[string]string{
							"meta.crossplane.io/maintainer":  "Upbound <support@upbound.io>",
							"meta.crossplane.io/source":      "github.com/upbound/project-getting-started",
							"meta.crossplane.io/license":     "Apache-2.0",
							"meta.crossplane.io/description": "Function xsubnetwork from project project-embedded-functions",
						},
					},
				},
			},
			// 3 APIs = 3 XRDs + 3 compositions.
			expectedObjectCount: 6,
			expectedAnnotatedLayers: map[string]bool{
				xpkg.SchemaKclAnnotation:    true,
				xpkg.SchemaPythonAnnotation: true,
				xpkg.PackageAnnotation:      true,
				xpkg.ExamplesAnnotation:     false, // no-examples expected
			},
		},
	}

	for testName, tc := range tcs {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			outFS := afero.NewMemMapFs()
			mockRunner := MockSchemaRunner{}

			c := &Cmd{
				ProjectFile:  "upbound.yaml",
				OutputDir:    "_output",
				NoBuildCache: true,

				projFS:             tc.projFS,
				outputFS:           outFS,
				functionIdentifier: functions.FakeIdentifier,
				schemaRunner:       mockRunner,
			}

			// Parse the upbound.yaml from the example so we can validate that certain
			// fields were copied correctly later in the test.
			var project v1alpha1.Project
			y, err := afero.ReadFile(c.projFS, "upbound.yaml")
			assert.NilError(t, err)
			err = yaml.Unmarshal(y, &project)
			assert.NilError(t, err)

			// Build the package.
			err = c.Run(context.Background(), &pterm.BasicTextPrinter{
				Style:  pterm.DefaultBasicText.Style,
				Writer: &TestWriter{t},
			})
			assert.NilError(t, err)

			// List the built packages load them from the output file.
			cfgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, ConfigurationTag))
			assert.NilError(t, err)
			opener := func() (io.ReadCloser, error) {
				return outFS.Open(tc.outputFile)
			}
			mfst, err := tarball.LoadManifest(opener)
			assert.NilError(t, err)

			var (
				fnImages = make(map[name.Repository][]v1.Image)
				cfgImage v1.Image
			)
			for _, desc := range mfst {
				if slices.Contains(desc.RepoTags, cfgTag.String()) {
					cfgImage, err = tarball.Image(opener, &cfgTag)
					assert.NilError(t, err)

					cfgImage, err = xpkg.AnnotateImage(cfgImage)
					if err != nil {
						t.Fatalf("Failed to annotate image: %v", err)
					}

					// Check for annotations in the image manifest layers
					manifest, err := cfgImage.Manifest()
					assert.NilError(t, err)

					foundLayers := map[string]bool{
						xpkg.SchemaKclAnnotation:    false,
						xpkg.SchemaPythonAnnotation: false,
						xpkg.PackageAnnotation:      false,
						xpkg.ExamplesAnnotation:     false,
					}

					// Iterate over manifest layers to find annotations
					for _, layer := range manifest.Layers {
						if value, ok := layer.Annotations[xpkg.AnnotationKey]; ok {
							// Mark the layer as found if it's an expected annotation
							if _, expected := foundLayers[value]; expected {
								foundLayers[value] = true
							}
						}
					}

					assert.DeepEqual(t, tc.expectedAnnotatedLayers, foundLayers)

				} else {
					fnTag, err := name.NewTag(desc.RepoTags[0])
					assert.NilError(t, err)
					fnImage, err := tarball.Image(opener, &fnTag)
					assert.NilError(t, err)
					fnImages[fnTag.Repository] = append(fnImages[fnTag.Repository], fnImage)
				}
			}

			// Validate the function packages and collect the dependencies that
			// should have been generated for the configuration.
			var fnDeps []xpmetav1.Dependency
			assert.Equal(t, len(tc.expectedFunctions), len(fnImages))
			for repo, images := range fnImages {
				// There should be two images, one for each of the two default
				// architectures.
				assert.Assert(t, cmp.Len(images, 2))
				image := images[0]

				m, err := xpkgmarshaler.NewMarshaler()
				assert.NilError(t, err)
				pkg, err := m.FromImage(xpkg.Image{
					Image: image,
				})
				assert.NilError(t, err)

				linter := xpkg.NewFunctionLinter()
				err = linter.Lint(&PackageAdapter{pkg})
				assert.NilError(t, err)

				// Build an index so we know the digest of the desired
				// dependency.
				idx, _, err := xpkg.BuildIndex(images...)
				assert.NilError(t, err)
				dgst, err := idx.Digest()
				assert.NilError(t, err)
				fnDeps = append(fnDeps, xpmetav1.Dependency{
					Function: ptr.To(repo.String()),
					Version:  dgst.String(),
				})

				fnMeta, ok := pkg.Meta().(*xpmetav1.Function)
				assert.Assert(t, ok, "unexpected metadata type for function")
				assert.Assert(t, cmp.Contains(tc.expectedFunctions, fnMeta))
			}

			// Validate the configuration package.
			m, err := xpkgmarshaler.NewMarshaler()
			assert.NilError(t, err)
			pkg, err := m.FromImage(xpkg.Image{
				Image: cfgImage,
			})
			assert.NilError(t, err)

			linter := xpkg.NewConfigurationLinter()
			err = linter.Lint(&PackageAdapter{pkg})
			assert.NilError(t, err)

			cfgMeta, ok := pkg.Meta().(*xpmetav1.Configuration)
			assert.Assert(t, ok, "unexpected metadata type for configuration")
			assert.DeepEqual(t, cfgMeta.TypeMeta, metav1.TypeMeta{
				APIVersion: xpmetav1.SchemeGroupVersion.String(),
				Kind:       xpmetav1.ConfigurationKind,
			})
			assert.DeepEqual(t, cfgMeta.ObjectMeta, metav1.ObjectMeta{
				Name: project.Name,
				Annotations: map[string]string{
					"meta.crossplane.io/maintainer":  project.Spec.Maintainer,
					"meta.crossplane.io/source":      project.Spec.Source,
					"meta.crossplane.io/license":     project.Spec.License,
					"meta.crossplane.io/description": project.Spec.Description,
					"meta.crossplane.io/readme":      project.Spec.Readme,
				},
			})
			assert.DeepEqual(t, cfgMeta.Spec.MetaSpec.Crossplane, project.Spec.Crossplane)
			// Validate that the configuration depends on all the project
			// dependencies and the embedded functions.
			assert.Assert(t, cmp.Len(cfgMeta.Spec.MetaSpec.DependsOn, len(project.Spec.DependsOn)+len(fnImages)))
			for _, dep := range project.Spec.DependsOn {
				assert.Assert(t, cmp.Contains(cfgMeta.Spec.MetaSpec.DependsOn, dep))
			}
			for _, dep := range fnDeps {
				assert.Assert(t, cmp.Contains(cfgMeta.Spec.MetaSpec.DependsOn, dep))
			}

			objs := pkg.Objects()
			// TODO(adamwg): Right now we generate CRDs during parsing and
			// inject them into the package, which doubles the object
			// count. This assertion will need to change when we refactor the
			// dependency manager to generate the CRDs after, rather than
			// during, package loading.
			assert.Assert(t, cmp.Len(objs, 2*tc.expectedObjectCount))
		})
	}
}

type MockSchemaRunner struct{}

func (m MockSchemaRunner) Generate(ctx context.Context, fs afero.Fs, folder string, imageName string, args []string) error {
	// Simulate generation for KCL schema files
	if strings.Contains(imageName, "kcl") { // Check for KCL specific marker, if any
		outputPath := "models/v1alpha1/platform_acme_co_v1alpha1_subnetwork.k"
		_ = fs.MkdirAll("models/v1alpha1/", os.ModePerm)
		return afero.WriteFile(fs, outputPath, []byte("mock KCL content"), os.ModePerm)
	}
	// Simulate generation for Python schema files
	outputPath := "models/workdir/platform_acme_co_v1alpha1_subnetwork/io/k8s/apimachinery/pkg/apis/meta/v1.py"
	_ = fs.MkdirAll("models/workdir/platform_acme_co_v1alpha1_subnetwork/io/k8s/apimachinery/pkg/apis/meta/", os.ModePerm)
	return afero.WriteFile(fs, outputPath, []byte("mock Python content"), os.ModePerm)
}

type TestWriter struct {
	t *testing.T
}

func (w *TestWriter) Write(b []byte) (int, error) {
	out := strings.TrimRight(string(b), "\n")
	w.t.Log(out)
	return len(b), nil
}

// PackageAdapter translates a `ParsedPackage` from the xpkg marshaler into a
// `linter.Package` so we can lint it.
type PackageAdapter struct {
	wrap *xpkgmarshaler.ParsedPackage
}

func (pkg *PackageAdapter) GetMeta() []runtime.Object {
	return []runtime.Object{pkg.wrap.Meta()}
}

func (pkg *PackageAdapter) GetObjects() []runtime.Object {
	return pkg.wrap.Objects()
}
