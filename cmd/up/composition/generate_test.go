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
	"context"
	"embed"
	"io/fs"
	"path/filepath"
	"testing"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"

	"k8s.io/utils/ptr"
)

var (
	//go:embed testdata/projectA/**
	projectAFS embed.FS

	//go:embed testdata/projectB/**
	projectBFS embed.FS

	//go:embed testdata/projectC/**
	projectCFS embed.FS

	//go:embed testdata/packages/*
	packagesFS embed.FS

	//go:embed testdata/packagesB/*
	packagesBFS embed.FS
)

func TestNewComposition(t *testing.T) {
	type want struct {
		composition *v1.Composition
		err         string
	}

	cases := map[string]struct {
		name       string
		plural     string
		packages   afero.Fs
		embeddedFS embed.FS
		want       want
	}{
		"CompositionWithAnnotationsAndName": {
			name:       "xyz",
			embeddedFS: projectAFS,
			packages:   afero.NewBasePathFs(afero.FromIOFS{FS: packagesFS}, "testdata/packages"),
			want: want{
				composition: &v1.Composition{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Composition",
						APIVersion: "apiextensions.crossplane.io/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xyz.xnetworks.aws.platform.upbound.io",
						Annotations: map[string]string{
							"cloud": "aws",
							"type":  "network",
						},
					},
					Spec: v1.CompositionSpec{
						CompositeTypeRef: v1.TypeReference{
							APIVersion: "aws.platform.upbound.io/v1alpha1", // Expected API version
							Kind:       "XNetwork",                         // Expected kind
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
		"CompositionWithoutAnnotations": {
			embeddedFS: projectBFS,
			packages:   afero.NewBasePathFs(afero.FromIOFS{FS: packagesBFS}, "testdata/packagesB"),
			want: want{
				composition: &v1.Composition{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Composition",
						APIVersion: "apiextensions.crossplane.io/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "xnetworks.aws.platform.upbound.io",
						Annotations: map[string]string{},
					},
					Spec: v1.CompositionSpec{
						CompositeTypeRef: v1.TypeReference{
							APIVersion: "aws.platform.upbound.io/v1alpha1", // Expected API version
							Kind:       "XNetwork",                         // Expected kind
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
		"CompositionWithCustomPlural": {
			plural:     "Xpostgreses",
			embeddedFS: projectBFS,
			packages:   afero.NewBasePathFs(afero.FromIOFS{FS: packagesBFS}, "testdata/packagesB"),
			want: want{
				composition: &v1.Composition{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Composition",
						APIVersion: "apiextensions.crossplane.io/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:        "xpostgreses.aws.platform.upbound.io",
						Annotations: map[string]string{},
					},
					Spec: v1.CompositionSpec{
						CompositeTypeRef: v1.TypeReference{
							APIVersion: "aws.platform.upbound.io/v1alpha1", // Expected API version
							Kind:       "XNetwork",                         // Expected kind
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
		"CompositionFromXRD": {
			embeddedFS: projectCFS,
			packages:   afero.NewBasePathFs(afero.FromIOFS{FS: packagesBFS}, "testdata/packagesB"),
			want: want{
				composition: &v1.Composition{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Composition",
						APIVersion: "apiextensions.crossplane.io/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xnetworks.aws.platform.upbound.io",
					},
					Spec: v1.CompositionSpec{
						CompositeTypeRef: v1.TypeReference{
							APIVersion: "aws.platform.upbound.io/v1alpha1", // Expected API version
							Kind:       "XNetwork",                         // Expected kind from plural
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

			outFS := afero.NewMemMapFs()
			// Set up a mock cache directory in Afero
			cch, err := cache.NewLocal("/cache", cache.WithFS(outFS))
			assert.NilError(t, err)

			// Create mock fetcher that holds the images
			r := image.NewResolver(
				image.WithFetcher(
					&image.FSFetcher{FS: tc.packages},
				),
			)

			// Initialize the dependency manager
			mgr, err := manager.New(
				manager.WithCache(cch),
				manager.WithResolver(r),
			)
			assert.NilError(t, err)

			// Embed test data into projectFS
			projFS := afero.NewMemMapFs()
			err = embedToAferoFS(tc.embeddedFS, projFS, "testdata", "/")

			// Parse project config
			proj, _ := project.Parse(projFS, "/upbound.yaml")
			assert.NilError(t, err)

			// Construct a workspace from the test filesystem.
			ws, err := workspace.New("/",
				workspace.WithFS(projFS),
				workspace.WithPermissiveParser(),
			)
			assert.NilError(t, err)
			err = ws.Parse(context.Background())
			assert.NilError(t, err)

			generateCmd := generateCmd{
				Name:     tc.name,
				Plural:   tc.plural,
				Resource: "/test.yaml",
				m:        mgr,
				ws:       ws,
				proj:     proj,
				projFS:   projFS,
				apisFS:   projFS,
			}

			// Call newComposition and check results
			got, _, err := generateCmd.newComposition(context.Background())

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
