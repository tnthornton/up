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

package dependency

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"testing"

	pkgmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

var (
	// NOTE: The dependency manager will try to recursively resolve
	// dependencies, but we can only load one package into the mock fetcher, so
	// we can't test with any packages that have dependencies. The function and
	// provider in testdata are real ones from marketplace (fetched with crane),
	// while the configuration is a fake one built without any dependencies.

	//go:embed testdata/function-auto-ready-v0.2.1.xpkg
	functionXpkgBytes []byte
	//go:embed testdata/provider-nop-v0.2.1.xpkg
	providerXpkgBytes []byte
	//go:embed testdata/configuration-empty-v0.1.0.xpkg
	configurationXpkgBytes []byte
)

type addTestCase struct {
	inputDeps    []pkgmetav1.Dependency
	newPackage   string
	image        v1.Image
	imageTag     name.Tag
	packageType  pkgv1beta1.PackageType
	expectedDeps []pkgmetav1.Dependency
}

func TestAdd(t *testing.T) {
	// Create images for use with the mock fetcher in tests.
	functionXpkg, err := tarball.Image(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(functionXpkgBytes)), nil
	}, nil)
	assert.NilError(t, err)
	functionTag, err := name.NewTag("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1")
	assert.NilError(t, err)
	providerXpkg, err := tarball.Image(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(providerXpkgBytes)), nil
	}, nil)
	assert.NilError(t, err)
	providerTag, err := name.NewTag("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1")
	assert.NilError(t, err)
	configurationXpkg, err := tarball.Image(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(configurationXpkgBytes)), nil
	}, nil)
	assert.NilError(t, err)
	configurationTag, err := name.NewTag("xpkg.upbound.io/example/configuration-empty:v0.1.0")
	assert.NilError(t, err)

	tcs := map[string]addTestCase{
		"AddFunctionWithoutVersion": {
			inputDeps:   nil,
			newPackage:  functionTag.RepositoryStr(),
			image:       functionXpkg,
			imageTag:    functionTag,
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To(functionTag.RepositoryStr()),
				Version:  functionTag.TagStr(),
			}},
		},
		"AddProviderWithoutVersion": {
			inputDeps:   nil,
			newPackage:  providerTag.RepositoryStr(),
			image:       providerXpkg,
			imageTag:    providerTag,
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Provider: ptr.To(providerTag.RepositoryStr()),
				Version:  providerTag.TagStr(),
			}},
		},
		"AddConfigurationWithoutVersion": {
			inputDeps:   nil,
			newPackage:  configurationTag.RepositoryStr(),
			image:       configurationXpkg,
			imageTag:    configurationTag,
			packageType: pkgv1beta1.ConfigurationPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Configuration: ptr.To(configurationTag.RepositoryStr()),
				Version:       configurationTag.TagStr(),
			}},
		},

		"AddFunctionWithVersion": {
			inputDeps:   nil,
			newPackage:  functionTag.RepositoryStr() + "@" + functionTag.TagStr(),
			image:       functionXpkg,
			imageTag:    functionTag,
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To(functionTag.RepositoryStr()),
				Version:  functionTag.TagStr(),
			}},
		},
		"AddProviderWithVersion": {
			inputDeps:   nil,
			newPackage:  providerTag.RepositoryStr() + "@" + providerTag.TagStr(),
			image:       providerXpkg,
			imageTag:    providerTag,
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Provider: ptr.To(providerTag.RepositoryStr()),
				Version:  providerTag.TagStr(),
			}},
		},
		"AddConfigurationWithVersion": {
			inputDeps:   nil,
			newPackage:  configurationTag.RepositoryStr() + "@" + configurationTag.TagStr(),
			image:       configurationXpkg,
			imageTag:    configurationTag,
			packageType: pkgv1beta1.ConfigurationPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Configuration: ptr.To(configurationTag.RepositoryStr()),
				Version:       configurationTag.TagStr(),
			}},
		},

		"AddProviderWithExistingDeps": {
			inputDeps: []pkgmetav1.Dependency{{
				Function: ptr.To(functionTag.RepositoryStr()),
				Version:  functionTag.TagStr(),
			}},
			newPackage:  providerTag.RepositoryStr() + "@" + providerTag.TagStr(),
			image:       providerXpkg,
			imageTag:    providerTag,
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{
				{
					Function: ptr.To(functionTag.RepositoryStr()),
					Version:  functionTag.TagStr(),
				},
				{
					Provider: ptr.To(providerTag.RepositoryStr()),
					Version:  providerTag.TagStr(),
				},
			},
		},
		"UpdateFunction": {
			inputDeps: []pkgmetav1.Dependency{{
				Function: ptr.To(functionTag.RepositoryStr()),
				Version:  "v0.1.0",
			}},
			newPackage:  functionTag.RepositoryStr() + "@" + functionTag.TagStr(),
			image:       functionXpkg,
			imageTag:    functionTag,
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To(functionTag.RepositoryStr()),
				Version:  functionTag.TagStr(),
			}},
		},
	}

	// Run each test for Projects, Configurations, Providers, and Functions,
	// since this command supports all package types.

	t.Run("Project", func(t *testing.T) {
		t.Parallel()

		for name, tc := range tcs {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				tc.Run(t, func(deps []pkgmetav1.Dependency) pkgmetav1.Pkg {
					return &v1alpha1.Project{
						TypeMeta: metav1.TypeMeta{
							APIVersion: v1alpha1.ProjectGroupVersionKind.GroupVersion().String(),
							Kind:       v1alpha1.ProjectKind,
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-project",
						},
						Spec: &v1alpha1.ProjectSpec{
							DependsOn: deps,
						},
					}
				})
			})
		}
	})

	t.Run("Configuration", func(t *testing.T) {
		t.Parallel()

		for name, tc := range tcs {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				tc.Run(t, func(deps []pkgmetav1.Dependency) pkgmetav1.Pkg {
					return &pkgmetav1.Configuration{
						TypeMeta: metav1.TypeMeta{
							APIVersion: pkgmetav1.SchemeGroupVersion.String(),
							Kind:       pkgmetav1.ConfigurationKind,
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-configuration",
						},
						Spec: pkgmetav1.ConfigurationSpec{
							MetaSpec: pkgmetav1.MetaSpec{
								DependsOn: deps,
							},
						},
					}
				})
			})
		}
	})

	t.Run("Provider", func(t *testing.T) {
		t.Parallel()

		for name, tc := range tcs {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				tc.Run(t, func(deps []pkgmetav1.Dependency) pkgmetav1.Pkg {
					return &pkgmetav1.Provider{
						TypeMeta: metav1.TypeMeta{
							APIVersion: pkgmetav1.SchemeGroupVersion.String(),
							Kind:       pkgmetav1.ProviderKind,
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-configuration",
						},
						Spec: pkgmetav1.ProviderSpec{
							MetaSpec: pkgmetav1.MetaSpec{
								DependsOn: deps,
							},
						},
					}
				})
			})
		}
	})

	t.Run("Function", func(t *testing.T) {
		t.Parallel()

		for name, tc := range tcs {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				tc.Run(t, func(deps []pkgmetav1.Dependency) pkgmetav1.Pkg {
					return &pkgmetav1.Function{
						TypeMeta: metav1.TypeMeta{
							APIVersion: pkgmetav1.SchemeGroupVersion.String(),
							Kind:       pkgmetav1.FunctionKind,
						},
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-configuration",
						},
						Spec: pkgmetav1.FunctionSpec{
							MetaSpec: pkgmetav1.MetaSpec{
								DependsOn: deps,
							},
						},
					}
				})
			})
		}
	})
}

func (tc *addTestCase) Run(t *testing.T, makePkg func(deps []pkgmetav1.Dependency) pkgmetav1.Pkg) {
	// Create test filesystem, populate project metadata file.
	fs := afero.NewMemMapFs()
	inputPkg := makePkg(tc.inputDeps)
	bs, err := yaml.Marshal(inputPkg)
	assert.NilError(t, err)
	err = afero.WriteFile(fs, "/project/meta.yaml", bs, 0644)
	assert.NilError(t, err)

	// Create inputs: cache, image resolver populated with the image.
	cch, err := cache.NewLocal("/cache", cache.WithFS(fs))
	assert.NilError(t, err)

	mt, err := tc.image.MediaType()
	assert.NilError(t, err)
	dgst, err := tc.image.Digest()
	assert.NilError(t, err)
	imgDesc := &v1.Descriptor{
		MediaType: mt,
		Digest:    dgst,
	}
	r := image.NewResolver(
		image.WithFetcher(
			image.NewMockFetcher(
				image.WithImage(tc.image),
				image.WithDescriptor(imgDesc),
				image.WithTags([]string{tc.imageTag.TagStr()}),
			),
		),
	)

	// Create a dependnecy manager that uses our cache and resolver.
	mgr, err := manager.New(
		manager.WithCache(cch),
		manager.WithResolver(r),
	)
	assert.NilError(t, err)

	// Construct a workspace from the test filesystem.
	ws, err := workspace.New("/project",
		workspace.WithFS(fs),
		workspace.WithPermissiveParser(),
	)
	assert.NilError(t, err)
	err = ws.Parse(context.Background())
	assert.NilError(t, err)

	// Add the dependency.
	cmd := &addCmd{
		m:       mgr,
		ws:      ws,
		Package: tc.newPackage,
	}
	err = cmd.Run(context.Background(), &pterm.DefaultBasicText, &pterm.DefaultBulletList)
	assert.NilError(t, err)

	// Verify that the dep was correctly added to the metadata.
	updatedBytes, err := afero.ReadFile(fs, "/project/meta.yaml")
	assert.NilError(t, err)

	var updated unstructured.Unstructured
	err = yaml.Unmarshal(updatedBytes, &updated)
	assert.NilError(t, err)

	var updatedPkg pkgmetav1.Pkg
	switch updated.GetKind() {
	case "Project":
		updatedPkg = &v1alpha1.Project{}
	case "Configuration":
		updatedPkg = &pkgmetav1.Configuration{}
	case "Provider":
		updatedPkg = &pkgmetav1.Provider{}
	case "Function":
		updatedPkg = &pkgmetav1.Function{}
	default:
		t.Errorf("unexpected metadata kind %s", updated.GetKind())
	}

	err = runtime.DefaultUnstructuredConverter.FromUnstructured(updated.UnstructuredContent(), updatedPkg)
	assert.NilError(t, err)
	assert.DeepEqual(t, tc.expectedDeps, updatedPkg.GetDependencies())

	// Verify that the dep was added to the cache.
	cchPkg, err := cch.Get(pkgv1beta1.Dependency{
		Package:     tc.imageTag.RepositoryStr(),
		Type:        tc.packageType,
		Constraints: tc.imageTag.TagStr(),
	})
	assert.NilError(t, err)
	assert.Assert(t, cchPkg != nil)
}
