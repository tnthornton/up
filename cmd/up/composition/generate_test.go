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
	_ "embed"
	"testing"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/upbound/up/pkg/apis/project/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// Embed the project and xrd YAML files.
//
//go:embed testdata/upbound.yaml
var projectYAML []byte

//go:embed testdata/definition.yaml
var xrdYAML []byte

//go:embed testdata/template.fn.crossplane.io_kclinputs.yaml
var kclYAML []byte

//go:embed testdata/gotemplating.fn.crossplane.io_gotemplates.yaml
var goTemplateYAML []byte

//go:embed testdata/pt.fn.crossplane.io_resources.yaml
var patYAML []byte

//go:embed testdata/cel.fn.crossplane.io_filters.yaml
var celYAML []byte

func TestNewComposition(t *testing.T) {
	type want struct {
		composition *v1.Composition
		err         string
	}

	// Precompute expected RawExtension values by calling setRawExtension
	var eRawExtKCL, eRawExtPat, eRawExtGoTemplate, eRawExtCel *runtime.RawExtension

	var kclCRD apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(kclYAML, &kclCRD); err != nil {
		t.Fatalf("Failed to unmarshal KCL CRD: %v", err)
	}

	var goTemplateCRD apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(goTemplateYAML, &goTemplateCRD); err != nil {
		t.Fatalf("Failed to unmarshal Patch-and-Transform CRD: %v", err)
	}

	var patCRD apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(patYAML, &patCRD); err != nil {
		t.Fatalf("Failed to unmarshal Patch-and-Transform CRD: %v", err)
	}

	var celCRD apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(celYAML, &celCRD); err != nil {
		t.Fatalf("Failed to unmarshal CEL CRD: %v", err)
	}

	// Initialize generateCmd with input type "filesystem"
	generateCmd := generateCmd{
		XRD:         "testdata/definition.yaml", // Mock path (not actually used in embedded)
		ProjectFile: "testdata/upbound.yaml",    // Mock path (not actually used in embedded)
	}

	// Generate expected RawExtension for KCLInput
	rawExtKCL, err := generateCmd.setRawExtension(kclCRD)
	if err != nil {
		t.Fatalf("Failed to set raw extension for KCLInput: %v", err)
	}
	eRawExtKCL = rawExtKCL

	// Generate expected RawExtension for KCLInput
	rawExtGoTemplate, err := generateCmd.setRawExtension(goTemplateCRD)
	if err != nil {
		t.Fatalf("Failed to set raw extension for KCLInput: %v", err)
	}
	eRawExtGoTemplate = rawExtGoTemplate

	// Generate expected RawExtension for Patch-and-Transform
	rawExtPat, err := generateCmd.setRawExtension(patCRD)
	if err != nil {
		t.Fatalf("Failed to set raw extension for Patch-and-Transform: %v", err)
	}
	eRawExtPat = rawExtPat

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
				var xrd v1.CompositeResourceDefinition
				err := yaml.Unmarshal(xrdYAML, &xrd)
				if err != nil {
					t.Fatalf("Failed to unmarshal XRD: %v", err)
				}
				return xrd
			}(),
			project: func() v1alpha1.Project {
				var project v1alpha1.Project
				err := yaml.Unmarshal(projectYAML, &project)
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
								Input: eRawExtKCL,
							},
							{
								Step: "crossplane-contrib-function-go-templating",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-go-templating",
								},
								// Precomputed expected RawExtension for Go-Template
								Input: eRawExtGoTemplate,
							},
							{
								Step: "crossplane-contrib-function-patch-and-transform",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-patch-and-transform",
								},
								// Precomputed expected RawExtension for Patch-and-Transform
								Input: eRawExtPat,
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
