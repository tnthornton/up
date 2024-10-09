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

package composition

import (
	"bytes"
	"context"
	"embed"
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-containerregistry/pkg/name"
	cv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

//go:embed testdata/projectA/*
var projectAFS embed.FS

//go:embed testdata/projectB/*
var projectBFS embed.FS

//go:embed testdata/function-auto-ready-v0.2.1.xpkg
var functionAutoReady []byte

//go:embed testdata/cel.fn.crossplane.io_filters.yaml
var celYAML []byte

func TestNewComposition(t *testing.T) {
	type want struct {
		composition *v1.Composition
		err         string
	}

	// Initialize the in-memory Afero filesystem
	fs := afero.NewMemMapFs()

	// Walk through the embedded testdata directory and copy files to the Afero filesystem
	err := embedToAferoFS(projectAFS, fs, "testdata", "/project")
	assert.NilError(t, err)

	// Precompute expected RawExtension values by calling setRawExtension
	var eRawExtCel *runtime.RawExtension

	var celCRD apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(celYAML, &celCRD); err != nil {
		t.Fatalf("Failed to unmarshal CEL CRD: %v", err)
	}

	// Initialize the workspace with the Afero filesystem
	ws, err := workspace.New("/project", workspace.WithFS(fs), workspace.WithPermissiveParser())
	assert.NilError(t, err)
	err = ws.Parse(context.Background()) // Parse the workspace
	assert.NilError(t, err)

	// Initialize the dependency manager
	mgr, err := manager.New()
	assert.NilError(t, err)

	// Construct a workspace from the test filesystem.
	ws, err = workspace.New("/project",
		workspace.WithFS(fs),
		workspace.WithPermissiveParser(),
	)
	assert.NilError(t, err)
	err = ws.Parse(context.Background())
	assert.NilError(t, err)

	// Initialize generateCmd with input type "filesystem"
	generateCmd := generateCmd{
		XRD:         "/project/definition.yaml",
		ProjectFile: "/project/upbound.yaml",
		m:           mgr,
		ws:          ws,
	}

	// Generate expected RawExtension for CEL
	rawExtCel, err := generateCmd.setRawExtension(celCRD)
	if err != nil {
		t.Fatalf("Failed to set raw extension for Patch-and-Transform: %v", err)
	}
	eRawExtCel = rawExtCel

	cases := map[string]struct {
		xrd     v1.CompositeResourceDefinition
		project v1alpha1.Project
		want    want
	}{
		"ValidInput": {
			xrd: func() v1.CompositeResourceDefinition {
				data, err := afero.ReadFile(fs, "/project/definition.yaml")
				if err != nil {
					t.Fatalf("Failed to read XRD file from afero filesystem: %v", err)
				}
				var xrd v1.CompositeResourceDefinition
				err = yaml.Unmarshal(data, &xrd)
				if err != nil {
					t.Fatalf("Failed to unmarshal XRD: %v", err)
				}
				return xrd
			}(),
			project: func() v1alpha1.Project {
				data, err := afero.ReadFile(fs, "/project/upbound.yaml")
				if err != nil {
					t.Fatalf("Failed to read project file from afero filesystem: %v", err)
				}
				var project v1alpha1.Project
				err = yaml.Unmarshal(data, &project)
				if err != nil {
					t.Fatalf("Failed to unmarshal project: %v", err)
				}
				return project
			}(),
			want: want{
				composition: &v1.Composition{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Composition",
						APIVersion: "apiextensions.crossplane.io/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xclusters.aws.upbound.io", // Expected output name
					},
					Spec: v1.CompositionSpec{
						CompositeTypeRef: v1.TypeReference{
							APIVersion: "aws.upbound.io/v1alpha1", // Expected API version
							Kind:       "XCluster",                // Expected kind
						},
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{
							{
								Step: "crossplane-contrib-function-kcl",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-kcl",
								},
								// Precomputed expected RawExtension for KCLInput
								Input: &runtime.RawExtension{
									Raw: []byte(kclTemplate),
								},
							},
							{
								Step: "crossplane-contrib-function-go-templating",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-go-templating",
								},
								// Precomputed expected RawExtension for Go-Template
								Input: &runtime.RawExtension{
									Raw: []byte(goTemplate),
								},
							},
							{
								Step: "crossplane-contrib-function-patch-and-transform",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-patch-and-transform",
								},
								// Precomputed expected RawExtension for Patch-and-Transform
								Input: &runtime.RawExtension{
									Raw: []byte(patTemplate),
								},
							},
							{
								Step: "crossplane-contrib-function-cel-filter",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-cel-filter",
								},
								// Precomputed expected RawExtension for CEL
								Input: eRawExtCel,
							},
							{
								Step: "crossplane-contrib-function-auto-ready",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-auto-ready",
								},
							},
						},
					},
				},
				err: "",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Call newComposition and check results
			got, err := generateCmd.newComposition(context.Background(), tc.xrd, tc.project)

			// Compare the result with the expected composition
			if diff := cmp.Diff(got, tc.want.composition, cmpopts.IgnoreUnexported(v1.Composition{})); diff != "" {
				t.Errorf("NewComposition() composition: -got, +want:\n%s", diff)
			}

			// Check for errors if there's an expected error or actual error occurred
			if err != nil || tc.want.err != "" {
				if diff := cmp.Diff(err.Error(), tc.want.err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("NewComposition() error: -got, +want:\n%s", diff)
				}
			}
		})
	}
}

func TestFunctionAutoReadyAddToCompositionAndProject(t *testing.T) {
	type want struct {
		composition *v1.Composition
		err         string
	}

	// Initialize the in-memory Afero filesystem
	fs := afero.NewMemMapFs()

	// Walk through the embedded testdata directory and copy files to the Afero filesystem
	err := embedToAferoFS(projectBFS, fs, "testdata", "/project")
	assert.NilError(t, err)

	// Set up a mock cache directory in Afero
	cch, err := cache.NewLocal("/cache", cache.WithFS(fs))
	assert.NilError(t, err)

	// 1. Create function-auto-ready image
	functionXpkg, err := tarball.Image(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(functionAutoReady)), nil
	}, nil)
	assert.NilError(t, err)
	functionTag, err := name.NewTag("xpkg.upbound.io/crossplane-contrib/function-cel-filter:v0.1.1")
	assert.NilError(t, err)

	// Create mock fetcher that holds the images

	mt, err := functionXpkg.MediaType()
	assert.NilError(t, err)
	dgst, err := functionXpkg.Digest()
	assert.NilError(t, err)
	imgDesc := &cv1.Descriptor{
		MediaType: mt,
		Digest:    dgst,
	}

	r := image.NewResolver(
		image.WithFetcher(
			image.NewMockFetcher(
				image.WithImage(functionXpkg),
				image.WithDescriptor(imgDesc),
				image.WithTags([]string{functionTag.TagStr()}),
			),
		),
	)

	// Initialize the workspace with the Afero filesystem
	ws, err := workspace.New("/project", workspace.WithFS(fs), workspace.WithPermissiveParser())
	assert.NilError(t, err)
	err = ws.Parse(context.Background()) // Parse the workspace
	assert.NilError(t, err)

	// Initialize the dependency manager
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

	// Initialize the generateCmd
	generateCmd := generateCmd{
		XRD:         "/project/definition.yaml",
		ProjectFile: "/project/upbound.yaml",
		m:           mgr,
		ws:          ws,
	}

	cases := map[string]struct {
		xrd     v1.CompositeResourceDefinition
		project v1alpha1.Project
		want    want
	}{
		"AddFunctionAutoReadyToCompositionAndProject": {
			xrd: func() v1.CompositeResourceDefinition {
				data, err := afero.ReadFile(fs, "/project/definition.yaml")
				if err != nil {
					t.Fatalf("Failed to read XRD file from afero filesystem: %v", err)
				}
				var xrd v1.CompositeResourceDefinition
				err = yaml.Unmarshal(data, &xrd)
				if err != nil {
					t.Fatalf("Failed to unmarshal XRD: %v", err)
				}
				return xrd
			}(),
			project: func() v1alpha1.Project {
				data, err := afero.ReadFile(fs, "/project/upbound.yaml")
				if err != nil {
					t.Fatalf("Failed to read project file from afero filesystem: %v", err)
				}
				var project v1alpha1.Project
				err = yaml.Unmarshal(data, &project)
				if err != nil {
					t.Fatalf("Failed to unmarshal project: %v", err)
				}
				return project
			}(),
			want: want{
				composition: &v1.Composition{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Composition",
						APIVersion: "apiextensions.crossplane.io/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xclusters.aws.upbound.io", // Expected output name
					},
					Spec: v1.CompositionSpec{
						CompositeTypeRef: v1.TypeReference{
							APIVersion: "aws.upbound.io/v1alpha1", // Expected API version
							Kind:       "XCluster",                // Expected kind
						},
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{
							{
								Step: "crossplane-contrib-function-auto-ready",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-auto-ready",
								},
							},
						},
					},
				},
				err: "",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Call newComposition and check results
			got, err := generateCmd.newComposition(context.Background(), tc.xrd, tc.project)

			// Compare the result with the expected composition
			if diff := cmp.Diff(got, tc.want.composition, cmpopts.IgnoreUnexported(v1.Composition{})); diff != "" {
				t.Errorf("NewComposition() composition: -got, +want:\n%s", diff)
			}

			// Check for errors if there's an expected error or actual error occurred
			if err != nil || tc.want.err != "" {
				if diff := cmp.Diff(err.Error(), tc.want.err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("NewComposition() error: -got, +want:\n%s", diff)
				}
			}
			// Verify the dependencies are updated in the upbound.yaml
			updatedBytes, err := afero.ReadFile(fs, "/project/upbound.yaml")
			assert.NilError(t, err)

			var updatedProject v1alpha1.Project
			err = yaml.Unmarshal(updatedBytes, &updatedProject)
			assert.NilError(t, err)

			foundDependency := false
			// Inline check for the specific dependency in Spec.DependsOn
			for _, dep := range updatedProject.Spec.DependsOn {
				if dep.Function != nil && *dep.Function == functionAutoReadyXpkg {
					foundDependency = true
					break
				}
			}

			// Assert that the expected dependency was found
			assert.Assert(t, foundDependency, "Expected dependency on function-auto-ready is missing")

		})
	}
}

// embedToAferoFS walks through an embedded FS and writes files to Afero FS
func embedToAferoFS(embeddedFS embed.FS, aferoFS afero.Fs, sourceDir string, targetDir string) error {
	err := fs.WalkDir(embeddedFS, sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Skip directories
		if d.IsDir() {
			return nil
		}
		// Read file content from the embedded FS
		data, err := embeddedFS.ReadFile(path)
		if err != nil {
			return err
		}
		// Write the file to the Afero filesystem at the target path
		targetPath := filepath.Join(targetDir, filepath.Base(path))
		err = afero.WriteFile(aferoFS, targetPath, data, 0644)
		if err != nil {
			return err
		}
		return nil
	})
	return err
}
