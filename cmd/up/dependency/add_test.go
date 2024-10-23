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
	"context"
	"embed"
	"testing"

	pkgmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	pkgv1beta1 "github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/google/go-containerregistry/pkg/name"
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
	//go:embed testdata/packages/*
	packagesFS embed.FS
)

type addTestCase struct {
	inputDeps    []pkgmetav1.Dependency
	newPackage   string
	imageTag     name.Tag
	packageType  pkgv1beta1.PackageType
	expectedDeps []pkgmetav1.Dependency
	expectError  bool // Add this field to indicate whether an error is expected

}

func TestAdd(t *testing.T) {
	tcs := map[string]addTestCase{
		"AddFunctionWithoutVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  ">=v0.0.0",
			}},
		},
		"AddProviderWithoutVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/provider-nop",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Provider: ptr.To("xpkg.upbound.io/crossplane-contrib/provider-nop"),
				Version:  ">=v0.0.0",
			}},
		},
		"AddConfigurationWithoutVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/configuration-empty",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/configuration-empty:v0.1.0").(name.Tag),
			packageType: pkgv1beta1.ConfigurationPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Configuration: ptr.To("xpkg.upbound.io/crossplane-contrib/configuration-empty"),
				Version:       ">=v0.0.0",
			}},
		},
		"AddFunctionWithVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.2.1",
			}},
		},
		"AddFunctionWithSemVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready@>=v0.1.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  ">=v0.1.0",
			}},
		},
		"AddFunctionWithSemVersionGreaterThan": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready@>v0.1.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  ">v0.1.0",
			}},
		},
		"AddFunctionWithSemVersionLessThan": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready@<v0.3.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "<v0.3.0",
			}},
		},
		"AddFunctionWithSemVersionLessThanError": {
			inputDeps:    nil,
			newPackage:   "xpkg.upbound.io/crossplane-contrib/function-auto-ready@<v0.2.0",
			imageTag:     name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType:  pkgv1beta1.FunctionPackageType,
			expectedDeps: nil,  // No dependencies should be added because of the version mismatch.
			expectError:  true, // Add this field to indicate this test expects an error.
		},
		"AddProviderWithVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/provider-nop@v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Provider: ptr.To("xpkg.upbound.io/crossplane-contrib/provider-nop"),
				Version:  "v0.2.1",
			}},
		},
		"AddProviderWithSemVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/provider-nop@<=v0.3.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Provider: ptr.To("xpkg.upbound.io/crossplane-contrib/provider-nop"),
				Version:  "<=v0.3.0",
			}},
		},
		"AddConfigurationWithVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/configuration-empty@v0.1.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/configuration-empty:v0.1.0").(name.Tag),
			packageType: pkgv1beta1.ConfigurationPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Configuration: ptr.To("xpkg.upbound.io/crossplane-contrib/configuration-empty"),
				Version:       "v0.1.0",
			}},
		},
		"AddConfigurationWithSemVersion": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/configuration-empty@<=v0.1.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/configuration-empty:v0.1.0").(name.Tag),
			packageType: pkgv1beta1.ConfigurationPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Configuration: ptr.To("xpkg.upbound.io/crossplane-contrib/configuration-empty"),
				Version:       "<=v0.1.0",
			}},
		},
		"AddConfigurationWithSemVersionNotAvailable": {
			inputDeps:    nil,
			newPackage:   "xpkg.upbound.io/crossplane-contrib/configuration-empty@>=v1.0.0",
			imageTag:     name.MustParseReference("xpkg.upbound.io/crossplane-contrib/configuration-empty:v0.1.0").(name.Tag),
			packageType:  pkgv1beta1.ConfigurationPackageType,
			expectedDeps: nil,  // No dependencies should be added because of the version mismatch.
			expectError:  true, // Add this field to indicate this test expects an error.
		},
		"AddProviderWithExistingDeps": {
			inputDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.2.1",
			}},
			newPackage:  "xpkg.upbound.io/crossplane-contrib/provider-nop@v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{
				{
					Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
					Version:  "v0.2.1",
				},
				{
					Provider: ptr.To("xpkg.upbound.io/crossplane-contrib/provider-nop"),
					Version:  "v0.2.1",
				},
			},
		},
		"UpdateFunction": {
			inputDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.1.0",
			}},
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.2.1",
			}},
		},
		"AddFunctionWithVersionColon": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.2.1",
			}},
		},
		"AddProviderWithVersionColon": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Provider: ptr.To("xpkg.upbound.io/crossplane-contrib/provider-nop"),
				Version:  "v0.2.1",
			}},
		},
		"AddConfigurationWithVersionColon": {
			inputDeps:   nil,
			newPackage:  "xpkg.upbound.io/crossplane-contrib/configuration-empty:v0.1.0",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/configuration-empty:v0.1.0").(name.Tag),
			packageType: pkgv1beta1.ConfigurationPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Configuration: ptr.To("xpkg.upbound.io/crossplane-contrib/configuration-empty"),
				Version:       "v0.1.0",
			}},
		},
		"AddProviderWithExistingDepsColon": {
			inputDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.2.1",
			}},
			newPackage:  "xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/provider-nop:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.ProviderPackageType,
			expectedDeps: []pkgmetav1.Dependency{
				{
					Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
					Version:  "v0.2.1",
				},
				{
					Provider: ptr.To("xpkg.upbound.io/crossplane-contrib/provider-nop"),
					Version:  "v0.2.1",
				},
			},
		},
		"UpdateFunctionColon": {
			inputDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.1.0",
			}},
			newPackage:  "xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1",
			imageTag:    name.MustParseReference("xpkg.upbound.io/crossplane-contrib/function-auto-ready:v0.2.1").(name.Tag),
			packageType: pkgv1beta1.FunctionPackageType,
			expectedDeps: []pkgmetav1.Dependency{{
				Function: ptr.To("xpkg.upbound.io/crossplane-contrib/function-auto-ready"),
				Version:  "v0.2.1",
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
	fs := afero.NewMemMapFs()
	inputPkg := makePkg(tc.inputDeps)
	bs, err := yaml.Marshal(inputPkg)
	assert.NilError(t, err)
	err = afero.WriteFile(fs, "/project/meta.yaml", bs, 0644)
	assert.NilError(t, err)

	ws, err := workspace.New("/project", workspace.WithFS(fs), workspace.WithPermissiveParser())
	assert.NilError(t, err)
	err = ws.Parse(context.Background())
	assert.NilError(t, err)

	cch, err := cache.NewLocal("/cache", cache.WithFS(fs))
	assert.NilError(t, err)

	testPkgFS := afero.NewBasePathFs(afero.FromIOFS{FS: packagesFS}, "testdata/packages")

	r := image.NewResolver(
		image.WithFetcher(
			&image.FSFetcher{FS: testPkgFS},
		),
	)

	mgr, err := manager.New(
		manager.WithCache(cch),
		manager.WithResolver(r),
	)
	assert.NilError(t, err)

	// Construct a workspace from the test filesystem.
	ws, err = workspace.New("/project",
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

	// Check if we expect an error.
	if tc.expectError {
		assert.ErrorContains(t, err, "supplied version does not match an existing version")
		return // No need to proceed with further checks if this is an error case.
	} else {
		assert.NilError(t, err)
	}

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
		Package:     tc.imageTag.RegistryStr() + "/" + tc.imageTag.RepositoryStr(),
		Type:        tc.packageType,
		Constraints: tc.imageTag.TagStr(),
	})
	assert.NilError(t, err)
	assert.Assert(t, cchPkg != nil)
}
