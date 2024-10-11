// Copyright 2022 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	"context"
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"k8s.io/utils/ptr"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"

	mxpkg "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
	"github.com/upbound/up/internal/xpkg/scheme"
	"github.com/upbound/up/internal/xpkg/snapshot/validator"
	"github.com/upbound/up/internal/xpkg/workspace"
)

func TestCompositionValidationResources(t *testing.T) {
	objScheme, _ := scheme.BuildObjectScheme()
	metaScheme, _ := scheme.BuildMetaScheme()
	ctx := context.Background()

	s := &Snapshot{
		objScheme:  objScheme,
		metaScheme: metaScheme,
		log:        logging.NewNopLogger(),
	}

	type args struct {
		data       runtime.Object
		validators map[schema.GroupVersionKind]validator.Validator
	}
	type want struct {
		result *validate.Result
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ComposedResourceMissingValidator": {
			reason: "Base resource GVK is missing a validator, we expect to get a warning indicating that.",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Resources: []v1.ComposedTemplate{
							{
								Base: runtime.RawExtension{Raw: []byte(`{"apiVersion": "database.aws.crossplane.io/v1beta1", "kind":"RDSInstance"}`)},
							},
						},
					},
				},
				validators: make(map[schema.GroupVersionKind]validator.Validator), // empty validators map
			},
			want: want{
				&validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.WarningTypeCode,
							Message:  "no definition found for resource (database.aws.crossplane.io/v1beta1, Kind=RDSInstance)",
							Name:     "spec.resources[0].base.apiVersion",
						},
					},
				},
			},
		},
		"ComposedResourceMissingRequiredField": {
			reason: "Base resource definition is missing a required field, we expect to get an error for that.",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Resources: []v1.ComposedTemplate{
							{
								Base: runtime.RawExtension{Raw: []byte(`{
									"apiVersion": "acm.aws.crossplane.io/v1alpha1",
									"kind":"Certificate",
									"spec": {
										"forProvider": {
											"domainName": "dn",
											"region": "us-west-2",
											"tags": [
												{"key": "k", "value": "v"}
											]
										},
										"writeConnectionSecretToRef": {
											"namespace": "default"
										}
									}
								}`)},
							},
						},
					},
				},
				validators: func() map[schema.GroupVersionKind]validator.Validator {
					v, _ := s.validatorsFromBytes(ctx, testSingleVersionCRD)
					return v
				}(),
			},
			want: want{
				&validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: 602,
							Message:  "spec.writeConnectionSecretToRef.name in body is required (acm.aws.crossplane.io/v1alpha1, Kind=Certificate)",
							Name:     "spec.resources[0].base.spec.writeConnectionSecretToRef.name",
						},
					},
				},
			},
		},
		"ComposedResourceRequiredFieldProvidedByPatch": {
			reason: "Base resource definition has a required field patched via patches.",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Resources: []v1.ComposedTemplate{
							{
								Base: runtime.RawExtension{Raw: []byte(`{
									"apiVersion": "acm.aws.crossplane.io/v1alpha1",
									"kind":"Certificate",
									"spec": {
										"forProvider": {
											"domainName": "dn",
											"region": "us-west-2",
											"tags": [
												{"key": "k", "value": "v"}
											]
										},
										"writeConnectionSecretToRef": {
											"namespace": "default"
										}
									}
								}`)},
								Patches: []v1.Patch{
									{
										FromFieldPath: ptr.To("metadata.uid"),
										ToFieldPath:   ptr.To("spec.writeConnectionSecretToRef.name"),
										Transforms: []v1.Transform{
											{
												Type: "string",
												String: &v1.StringTransform{
													Format: ptr.To("%s-postgresql"),
												},
											},
										},
									},
								},
							},
						},
					},
				},
				validators: func() map[schema.GroupVersionKind]validator.Validator {
					v, _ := s.validatorsFromBytes(ctx, testSingleVersionCRD)
					return v
				}(),
			},
			want: want{
				result: &validate.Result{Errors: []error{}},
			},
		},
		"ComposedResourceRequiredFieldProvidedByPatchSet": {
			reason: "Base resource definition has a required field patched via patchSet.",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						PatchSets: []v1.PatchSet{
							{
								Name: "connectionSecretRef",
								Patches: []v1.Patch{
									{
										FromFieldPath: ptr.To("metadata.uid"),
										ToFieldPath:   ptr.To("spec.writeConnectionSecretToRef.name"),
										Transforms: []v1.Transform{
											{
												Type: "string",
												String: &v1.StringTransform{
													Format: ptr.To("%s-postgresql"),
												},
											},
										},
									},
								},
							},
						},
						Resources: []v1.ComposedTemplate{
							{
								Base: runtime.RawExtension{Raw: []byte(`{
									"apiVersion": "acm.aws.crossplane.io/v1alpha1",
									"kind":"Certificate",
									"spec": {
										"forProvider": {
											"domainName": "dn",
											"region": "us-west-2",
											"tags": [
												{"key": "k", "value": "v"}
											]
										},
										"writeConnectionSecretToRef": {
											"namespace": "default"
										}
									}
								}`)},
								Patches: []v1.Patch{
									{
										Type:         v1.PatchTypePatchSet,
										PatchSetName: ptr.To("connectionSecretRef"),
									},
								},
							},
						},
					},
				},
				validators: func() map[schema.GroupVersionKind]validator.Validator {
					v, _ := s.validatorsFromBytes(ctx, testSingleVersionCRD)
					return v
				}(),
			},
			want: want{
				result: &validate.Result{Errors: []error{}},
			},
		},
		"ComposedResourceRequiredFieldProvidedByPatchThroughWriteConnectionSecretToRef": {
			reason: "Base resource definition has its writeConnectionSecretToRef namespace and name coming from the XR/C",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Resources: []v1.ComposedTemplate{
							{
								Base: runtime.RawExtension{Raw: []byte(`{
									"apiVersion": "acm.aws.crossplane.io/v1alpha1",
									"kind":"Certificate",
									"spec": {
										"forProvider": {
											"domainName": "dn",
											"region": "us-west-2",
											"tags": [
												{"key": "k", "value": "v"}
											]
										},
										"writeConnectionSecretToRef": {
												"namespace": "default"
										}
									}
								}`)},
								Patches: []v1.Patch{
									{
										FromFieldPath: ptr.To("spec.writeConnectionSecretToRef.name"),
										ToFieldPath:   ptr.To("spec.writeConnectionSecretToRef.name"),
									},
								},
							},
						},
					},
				},
				validators: func() map[schema.GroupVersionKind]validator.Validator {
					v, _ := s.validatorsFromBytes(ctx, testSingleVersionCRD)
					return v
				}(),
			},
			want: want{
				&validate.Result{
					Errors: []error{},
				},
			},
		},
		"ComposedResourceHasMixedNamingResources": {
			reason: "Base resources must either be all named or all not named.",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Resources: []v1.ComposedTemplate{
							{
								Name: ptr.To("r1"),
							},
							{},
						},
					},
				},
				validators: func() map[schema.GroupVersionKind]validator.Validator {
					v, _ := s.validatorsFromBytes(ctx, testSingleVersionCRD)
					return v
				}(),
			},
			want: want{
				&validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.ErrorTypeCode,
							Message:  "spec.resources[1].name: Required value: cannot mix named and anonymous resources, all resources must have a name or none must have a name",
							Name:     "spec.resources",
						},
					},
				},
			},
		},
		"ComposedResourceHasDuplicateNamedResources": {
			reason: "Base resources must be uniquely named.",
			args: args{
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Resources: []v1.ComposedTemplate{
							{
								Name: ptr.To("r1"),
							},
							{
								Name: ptr.To("r1"),
							},
						},
					},
				},
				validators: func() map[schema.GroupVersionKind]validator.Validator {
					v, _ := s.validatorsFromBytes(ctx, testSingleVersionCRD)
					return v
				}(),
			},
			want: want{
				&validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.ErrorTypeCode,
							Message:  `spec.resources[1].name: Duplicate value: "r1"`,
							Name:     "spec.resources",
						},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s.validators = tc.args.validators

			// convert runtime.Object -> *unstructured.Unstructured
			b, err := json.Marshal(tc.args.data)
			// we shouldn't see an error from Marshaling
			if diff := cmp.Diff(err, nil, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nCompositionValidation(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			var u unstructured.Unstructured
			json.Unmarshal(b, &u)

			v, _ := DefaultCompositionValidators(s)

			result := v.Validate(ctx, &u)

			if diff := cmp.Diff(tc.want.result, result); diff != "" {
				t.Errorf("\n%s\nCompositionValidation(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}

var (
	//go:embed testdata/upbound.yaml
	projectFile []byte
)

func TestCompositionValidationPipeline(t *testing.T) {
	objScheme, _ := scheme.BuildObjectScheme()
	metaScheme, _ := scheme.BuildMetaScheme()
	ctx := context.Background()

	s := &Snapshot{
		objScheme:  objScheme,
		metaScheme: metaScheme,
		log:        logging.NewNopLogger(),
	}

	type args struct {
		workspace  *workspace.Workspace
		data       runtime.Object
		validators map[schema.GroupVersionKind]validator.Validator
		packages   map[string]*mxpkg.ParsedPackage
	}
	type want struct {
		result *validate.Result
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ValidPipeline": {
			reason: "Validator should not return errors when a pipeline is valid.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					_ = f.MkdirAll("/functions/my-function", 0755)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{
							// Embedded function step.
							{
								Step: "my-function",
								FunctionRef: v1.FunctionReference{
									Name: "upbound-project-getting-startedmy-function",
								},
							},
							// Normal function step.
							{
								Step: "auto-ready",
								FunctionRef: v1.FunctionReference{
									Name: "crossplane-contrib-function-auto-ready",
								},
							},
						},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{},
				},
			},
		},
		"FunctionRefMissingDependency": {
			reason: "Pipeline functions should refer to a package dependency.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{{
							Step: "invalid",
							FunctionRef: v1.FunctionReference{
								Name: "acme-co-custom-function",
							},
						}},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.WarningTypeCode,
							Message:  `package does not depend on function "acme-co-custom-function"`,
							Name:     "spec.pipeline[0].functionRef.name",
						},
					},
				},
			},
		},
		"FunctionRefInputToFunctionWithNoInput": {
			reason: "Providing input to a function with no input type is an error.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				packages: map[string]*mxpkg.ParsedPackage{
					"xpkg.upbound.io/crossplane-contrib/function-auto-ready": {
						PType: v1beta1.FunctionPackageType,
						Objs:  []runtime.Object{},
					},
				},
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{{
							Step: "auto-ready",
							FunctionRef: v1.FunctionReference{
								Name: "crossplane-contrib-function-auto-ready",
							},
							Input: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion": "v1"}`),
							},
						}},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.WarningTypeCode,
							Message:  `function "crossplane-contrib-function-auto-ready" does not take input`,
							Name:     "spec.pipeline[0].input",
						},
					},
				},
			},
		},
		"FunctionRefUnparsableInput": {
			reason: "Providing malformed input to a function is an error.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				packages: map[string]*mxpkg.ParsedPackage{
					"xpkg.upbound.io/crossplane-contrib/function-auto-ready": {
						PType: v1beta1.FunctionPackageType,
						Objs: []runtime.Object{
							&apiextv1.CustomResourceDefinition{
								TypeMeta: apimetav1.TypeMeta{
									APIVersion: apiextv1.SchemeGroupVersion.String(),
									Kind:       "CustomResourceDefinition",
								},
								ObjectMeta: apimetav1.ObjectMeta{
									Name: "input.my-function.com",
								},
								Spec: apiextv1.CustomResourceDefinitionSpec{
									Group: "my-function.com",
									Names: apiextv1.CustomResourceDefinitionNames{
										Plural:   "inputs",
										Singular: "input",
										Kind:     "Input",
										ListKind: "InputList",
									},
									Versions: []apiextv1.CustomResourceDefinitionVersion{{
										Name:    "v1alpha1",
										Served:  true,
										Storage: true,
										Schema: &apiextv1.CustomResourceValidation{
											OpenAPIV3Schema: &apiextv1.JSONSchemaProps{},
										},
									}},
								},
							},
						},
					},
				},
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{{
							Step: "auto-ready",
							FunctionRef: v1.FunctionReference{
								Name: "crossplane-contrib-function-auto-ready",
							},
							Input: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion": "v1"}`),
							},
						}},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.WarningTypeCode,
							Message:  `Object 'Kind' is missing in '{"apiVersion":"v1"}'`,
							Name:     "spec.pipeline[0].input",
						},
					},
				},
			},
		},
		"FunctionRefWrongInputKind": {
			reason: "Providing the wrong kind of input to a function is an error.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				packages: map[string]*mxpkg.ParsedPackage{
					"xpkg.upbound.io/crossplane-contrib/function-auto-ready": {
						PType: v1beta1.FunctionPackageType,
						Objs: []runtime.Object{
							&apiextv1.CustomResourceDefinition{
								TypeMeta: apimetav1.TypeMeta{
									APIVersion: apiextv1.SchemeGroupVersion.String(),
									Kind:       "CustomResourceDefinition",
								},
								ObjectMeta: apimetav1.ObjectMeta{
									Name: "input.my-function.com",
								},
								Spec: apiextv1.CustomResourceDefinitionSpec{
									Group: "my-function.com",
									Names: apiextv1.CustomResourceDefinitionNames{
										Plural:   "inputs",
										Singular: "input",
										Kind:     "Input",
										ListKind: "InputList",
									},
									Versions: []apiextv1.CustomResourceDefinitionVersion{{
										Name:    "v1alpha1",
										Served:  true,
										Storage: true,
										Schema: &apiextv1.CustomResourceValidation{
											OpenAPIV3Schema: &apiextv1.JSONSchemaProps{},
										},
									}},
								},
							},
						},
					},
				},
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{{
							Step: "auto-ready",
							FunctionRef: v1.FunctionReference{
								Name: "crossplane-contrib-function-auto-ready",
							},
							Input: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion": "v1", "kind": "NotInput"}`),
							},
						}},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: validator.WarningTypeCode,
							Message:  `incorrect input type for step "auto-ready"; valid inputs: [my-function.com/v1alpha1, Kind=Input]`,
							Name:     "spec.pipeline[0].input.apiVersion",
						},
					},
				},
			},
		},
		"FunctionRefInvalidInput": {
			reason: "Invalid input to a function step should produce a validation error.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				packages: map[string]*mxpkg.ParsedPackage{
					"xpkg.upbound.io/crossplane-contrib/function-auto-ready": {
						PType: v1beta1.FunctionPackageType,
						Objs: []runtime.Object{
							&apiextv1.CustomResourceDefinition{
								TypeMeta: apimetav1.TypeMeta{
									APIVersion: apiextv1.SchemeGroupVersion.String(),
									Kind:       "CustomResourceDefinition",
								},
								ObjectMeta: apimetav1.ObjectMeta{
									Name: "input.my-function.com",
								},
								Spec: apiextv1.CustomResourceDefinitionSpec{
									Group: "my-function.com",
									Names: apiextv1.CustomResourceDefinitionNames{
										Plural:   "inputs",
										Singular: "input",
										Kind:     "Input",
										ListKind: "InputList",
									},
									Versions: []apiextv1.CustomResourceDefinitionVersion{{
										Name:    "v1alpha1",
										Served:  true,
										Storage: true,
										Schema: &apiextv1.CustomResourceValidation{
											OpenAPIV3Schema: &apiextv1.JSONSchemaProps{
												Properties: map[string]apiextv1.JSONSchemaProps{
													"apiVersion": {
														Type: "string",
													},
													"kind": {
														Type: "string",
													},
													"boolField": {
														Type: "boolean",
													},
												},
											},
										},
									}},
								},
							},
						},
					},
				},
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{{
							Step: "auto-ready",
							FunctionRef: v1.FunctionReference{
								Name: "crossplane-contrib-function-auto-ready",
							},
							Input: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion": "my-function.com/v1alpha1", "kind": "Input", "boolField": "asdf"}`),
							},
						}},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{
						&validator.Validation{
							TypeCode: 601, // This is a type code from the openapi validation library.
							Message:  `boolField in body must be of type boolean: "string" (my-function.com/v1alpha1, Kind=Input)`,
							Name:     "spec.pipeline[0].input.boolField",
						},
					},
				},
			},
		},
		"FunctionRefValidInput": {
			reason: "A pipeline step with valid input should not produce errors.",
			args: args{
				workspace: func() *workspace.Workspace {
					f := afero.NewMemMapFs()
					_ = afero.WriteFile(f, "/upbound.yaml", projectFile, 0644)
					ws, _ := workspace.New("/", workspace.WithFS(f))
					return ws
				}(),
				packages: map[string]*mxpkg.ParsedPackage{
					"xpkg.upbound.io/crossplane-contrib/function-auto-ready": {
						PType: v1beta1.FunctionPackageType,
						Objs: []runtime.Object{
							&apiextv1.CustomResourceDefinition{
								TypeMeta: apimetav1.TypeMeta{
									APIVersion: apiextv1.SchemeGroupVersion.String(),
									Kind:       "CustomResourceDefinition",
								},
								ObjectMeta: apimetav1.ObjectMeta{
									Name: "input.my-function.com",
								},
								Spec: apiextv1.CustomResourceDefinitionSpec{
									Group: "my-function.com",
									Names: apiextv1.CustomResourceDefinitionNames{
										Plural:   "inputs",
										Singular: "input",
										Kind:     "Input",
										ListKind: "InputList",
									},
									Versions: []apiextv1.CustomResourceDefinitionVersion{{
										Name:    "v1alpha1",
										Served:  true,
										Storage: true,
										Schema: &apiextv1.CustomResourceValidation{
											OpenAPIV3Schema: &apiextv1.JSONSchemaProps{
												Properties: map[string]apiextv1.JSONSchemaProps{
													"apiVersion": {
														Type: "string",
													},
													"kind": {
														Type: "string",
													},
													"boolField": {
														Type: "boolean",
													},
												},
											},
										},
									}},
								},
							},
						},
					},
				},
				data: &v1.Composition{
					TypeMeta: apimetav1.TypeMeta{
						Kind:       v1.CompositionKind,
						APIVersion: v1.SchemeGroupVersion.String(),
					},
					Spec: v1.CompositionSpec{
						Mode: ptr.To(v1.CompositionModePipeline),
						Pipeline: []v1.PipelineStep{{
							Step: "auto-ready",
							FunctionRef: v1.FunctionReference{
								Name: "crossplane-contrib-function-auto-ready",
							},
							Input: &runtime.RawExtension{
								Raw: []byte(`{"apiVersion": "my-function.com/v1alpha1", "kind": "Input", "boolField": true}`),
							},
						}},
					},
				},
			},
			want: want{
				result: &validate.Result{
					Errors: []error{},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s.w = tc.args.workspace
			if err := s.w.Parse(ctx); err != nil {
				t.Fatalf("failed to parse workspace for test: %v", err)
			}
			s.wsview = s.w.View()
			s.validators = tc.args.validators
			s.packages = tc.args.packages

			// convert runtime.Object -> *unstructured.Unstructured
			b, err := json.Marshal(tc.args.data)
			// we shouldn't see an error from Marshaling
			if diff := cmp.Diff(err, nil, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nCompositionValidation(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			var u unstructured.Unstructured
			json.Unmarshal(b, &u)

			v, _ := DefaultCompositionValidators(s)

			result := v.Validate(ctx, &u)

			if diff := cmp.Diff(tc.want.result, result); diff != "" {
				t.Errorf("\n%s\nCompositionValidation(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}
