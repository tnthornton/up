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

package xrd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestInferProperty tests the inferProperty function.
func TestInferProperty(t *testing.T) {
	type want struct {
		output extv1.JSONSchemaProps
		err    error
	}

	cases := map[string]struct {
		input interface{}
		want  want
	}{
		"StringType": {
			input: "hello",
			want: want{
				output: extv1.JSONSchemaProps{Type: "string"},
				err:    nil,
			},
		},
		"IntegerType": {
			input: 42,
			want: want{
				output: extv1.JSONSchemaProps{Type: "integer"},
				err:    nil,
			},
		},
		"FloatType": {
			input: 3.14,
			want: want{
				output: extv1.JSONSchemaProps{Type: "number"},
				err:    nil,
			},
		},
		"BooleanType": {
			input: true,
			want: want{
				output: extv1.JSONSchemaProps{Type: "boolean"},
				err:    nil,
			},
		},
		"ObjectType": {
			input: map[string]interface{}{
				"key": "value",
			},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]extv1.JSONSchemaProps{
						"key": {Type: "string"},
					},
				},
				err: nil,
			},
		},
		"ArrayTypeWithElements": {
			input: []interface{}{"one", "two"},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: "array",
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{Type: "string"},
					},
				},
				err: nil,
			},
		},
		"ArrayTypeEmpty": {
			input: []interface{}{},
			want: want{
				output: extv1.JSONSchemaProps{
					Type: "array",
					Items: &extv1.JSONSchemaPropsOrArray{
						Schema: &extv1.JSONSchemaProps{Type: "object"},
					},
				},
				err: nil,
			},
		},
		"UnknownType": {
			input: nil,
			want: want{
				output: extv1.JSONSchemaProps{Type: "string"},
				err:    nil,
			},
		},
		"ArrayWithMixedTypes": {
			input: []interface{}{1, "2", true},
			want: want{
				output: extv1.JSONSchemaProps{},
				err:    errors.New("mixed types detected in array"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := inferProperty(tc.input)

			// Compare errors
			if err != nil || tc.want.err != nil {
				if err == nil || tc.want.err == nil || err.Error() != tc.want.err.Error() {
					t.Errorf("inferProperty() error = %v, wantErr %v", err, tc.want.err)
				}
				return
			}

			// Compare the output
			if diff := cmp.Diff(got, tc.want.output); diff != "" {
				t.Errorf("inferProperty() -got, +want:\n%s", diff)
			}
		})
	}
}

// TestInferProperties tests the inferProperties function.
func TestInferProperties(t *testing.T) {
	type want struct {
		output map[string]extv1.JSONSchemaProps
		err    error
	}

	cases := map[string]struct {
		input map[string]interface{}
		want  want
	}{
		"SimpleObject": {
			input: map[string]interface{}{
				"key1": "value1",
				"key2": 42,
			},
			want: want{
				output: map[string]extv1.JSONSchemaProps{
					"key1": {Type: "string"},
					"key2": {Type: "integer"},
				},
				err: nil,
			},
		},
		"NestedObject": {
			input: map[string]interface{}{
				"nested": map[string]interface{}{
					"key": true,
				},
			},
			want: want{
				output: map[string]extv1.JSONSchemaProps{
					"nested": {
						Type: "object",
						Properties: map[string]extv1.JSONSchemaProps{
							"key": {Type: "boolean"},
						},
					},
				},
				err: nil,
			},
		},
		"ArrayInObject": {
			input: map[string]interface{}{
				"array": []interface{}{"a", "b"},
			},
			want: want{
				output: map[string]extv1.JSONSchemaProps{
					"array": {
						Type: "array",
						Items: &extv1.JSONSchemaPropsOrArray{
							Schema: &extv1.JSONSchemaProps{Type: "string"},
						},
					},
				},
				err: nil,
			},
		},
		"ObjectWithMixedArray": {
			input: map[string]interface{}{
				"array": []interface{}{1, "2"},
			},
			want: want{
				output: nil,
				err:    errors.New("error inferring property for key 'array': mixed types detected in array"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := inferProperties(tc.input)

			// Compare errors
			if err != nil || tc.want.err != nil {
				if err == nil || tc.want.err == nil || err.Error() != tc.want.err.Error() {
					t.Errorf("inferProperties() error = %v, wantErr %v", err, tc.want.err)
				}
				return
			}

			// Compare the output
			if diff := cmp.Diff(got, tc.want.output); diff != "" {
				t.Errorf("inferProperties() -got, +want:\n%s", diff)
			}
		})
	}
}

// TestNewXRD tests the newXRD function.
func TestNewXRD(t *testing.T) {
	type want struct {
		xrd *v1.CompositeResourceDefinition
		err error
	}

	cases := map[string]struct {
		inputYAML    string
		customPlural string
		want         want
	}{
		"XRXEKS": {
			inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  parameters:
    id: test
    region: eu-central-1
`,
			customPlural: "xeks",
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "XEKSSpec defines the desired state of XEKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id": {
																Type: "string",
															},
															"region": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "XEKSStatus defines the observed state of XEKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
				err: nil,
			},
		},
		"XRCEKS": {
			inputYAML: `
apiVersion: aws.u5d.io/v1
kind: EKS
metadata:
  name: test
  namespace: test-namespace
spec:
  parameters:
    id: test
    region: eu-central-1
`,
			customPlural: "eks",
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "EKS is the Schema for the EKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "EKSSpec defines the desired state of EKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id": {
																Type: "string",
															},
															"region": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "EKSStatus defines the observed state of EKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
						ClaimNames: &extv1.CustomResourceDefinitionNames{
							Kind:   "EKS",
							Plural: "eks",
						},
					},
				},
				err: nil,
			},
		},
		"XRPostgres": {
			inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
			customPlural: "Postgreses",
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "postgreses.database.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "database.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "Postgres",
							Plural:     "postgreses",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "Postgres is the Schema for the Postgres API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "PostgresSpec defines the desired state of Postgres.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"version": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "PostgresStatus defines the observed state of Postgres.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
				err: nil,
			},
		},
		"XRBucket": {
			inputYAML: `
apiVersion: storage.u5d.io/v1
kind: Bucket
metadata:
  name: test
spec:
  parameters:
    storage: "13"
`,
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "buckets.storage.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "storage.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "Bucket",
							Plural:     "buckets",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "Bucket is the Schema for the Bucket API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "BucketSpec defines the desired state of Bucket.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"storage": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "BucketStatus defines the observed state of Bucket.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
				err: nil,
			},
		},
		"XRBucketWithStatus": {
			inputYAML: `
apiVersion: storage.u5d.io/v1
kind: Bucket
metadata:
  name: test
spec:
  parameters:
    storage: "13"
status:
  bucketName: test
`,
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "buckets.storage.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "storage.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "Bucket",
							Plural:     "buckets",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "Bucket is the Schema for the Bucket API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "BucketSpec defines the desired state of Bucket.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"storage": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "BucketStatus defines the observed state of Bucket.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"bucketName": {
														Type: "string",
													},
												},
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
				err: nil,
			},
		},
		"MixedTypesInArray": {
			inputYAML: `
apiVersion: aws.u5d.io/v1
kind: MyClaim
metadata:
  name: my-claim
spec:
  parameters:
    - 1
    - "2"
    - true
`,
			customPlural: "myclaims",
			want: want{
				xrd: nil,
				err: errors.New("failed to infer properties for spec: error inferring property for key 'parameters': mixed types detected in array"),
			},
		},
		"NestedMixedTypesInArray": {
			inputYAML: `
apiVersion: aws.u5d.io/v1
kind: MyClaim
metadata:
  name: my-claim
spec:
  parameters:
    chris:
      - 1
      - "2"
      - true
`,
			customPlural: "myclaims",
			want: want{
				xrd: nil,
				err: errors.New("failed to infer properties for spec: error inferring property for key 'parameters': error inferring properties for object: error inferring property for key 'chris': mixed types detected in array"),
			},
		},
		"MissingAPIVersion": {
			inputYAML: `
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
			customPlural: "postgreses",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: apiVersion is required"),
			},
		},
		"MissingAPIVersionVersion": {
			inputYAML: `
apiVersion: database.u5d.io
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
			customPlural: "postgreses",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: apiVersion must be in the format group/version"),
			},
		},
		"MissingKind": {
			inputYAML: `
apiVersion: database.u5d.io/v1
metadata:
  name: test
spec:
  parameters:
    version: "13"
`,
			customPlural: "postgreses",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: kind is required"),
			},
		},
		"MissingMetadataName": {
			inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
spec:
  parameters:
    version: "13"
`,
			customPlural: "postgreses",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: metadata.name is required"),
			},
		},
		"MissingSpec": {
			inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
`,
			customPlural: "postgreses",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: spec is required"),
			},
		},
		"InvalidTopLevelKey": {
			inputYAML: `
apiVersion: database.u5d.io/v1
kind: Postgres
metadata:
  name: test
spec:
  parameters:
    version: "13"
invalidKey: shouldNotBeHere
`,
			customPlural: "postgreses",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: only apiVersion, kind, metadata, spec, and status are allowed as top-level keys"),
			},
		},
		"InvalidAPIVersionMultipleSlashes": {
			inputYAML: `
apiVersion: invalid/group/version/v1
kind: InvalidResource
metadata:
  name: test
spec:
  parameters:
    key: value
`,
			customPlural: "invalidresources",
			want: want{
				xrd: nil,
				err: errors.New("invalid manifest: apiVersion must be in the format group/version"),
			},
		},
		"RemoveXPStandardFieldsFromSpec": {
			inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  parameters:
    id: test
    region: eu-central-1
  resourceRefs:
    - name: resource1
  writeConnectionSecretToRef:
    name: secret
  publishConnectionDetailsTo:
    name: details
  environmentConfigRefs:
    - name: config1
  compositionSelector:
    matchLabels:
      layer: functions
`,
			customPlural: "xeks",
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "XEKSSpec defines the desired state of XEKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id": {
																Type: "string",
															},
															"region": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "XEKSStatus defines the observed state of XEKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
				err: nil,
			},
		},
		"RemoveOtherXPStandardFieldsFromSpec": {
			inputYAML: `
apiVersion: aws.u5d.io/v1
kind: XEKS
metadata:
  name: test
spec:
  parameters:
    id: test
    region: eu-central-1
  compositionRef:
    name: test-composition
  compositionRevisionRef:
    name: test-revision
  claimRef:
    name: test-claim
`,
			customPlural: "xeks",
			want: want{
				xrd: &v1.CompositeResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.crossplane.io/v1",
						Kind:       "CompositeResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.u5d.io",
					},
					Spec: v1.CompositeResourceDefinitionSpec{
						Group: "aws.u5d.io",
						Names: extv1.CustomResourceDefinitionNames{
							Categories: []string{"crossplane"},
							Kind:       "XEKS",
							Plural:     "xeks",
						},
						Versions: []v1.CompositeResourceDefinitionVersion{
							{
								Name:          "v1",
								Referenceable: true,
								Served:        true,
								Schema: &v1.CompositeResourceValidation{
									OpenAPIV3Schema: jsonSchemaPropsToRawExtension(&extv1.JSONSchemaProps{
										Description: "XEKS is the Schema for the XEKS API.",
										Type:        "object",
										Properties: map[string]extv1.JSONSchemaProps{
											"spec": {
												Description: "XEKSSpec defines the desired state of XEKS.",
												Type:        "object",
												Properties: map[string]extv1.JSONSchemaProps{
													"parameters": {
														Type: "object",
														Properties: map[string]extv1.JSONSchemaProps{
															"id": {
																Type: "string",
															},
															"region": {
																Type: "string",
															},
														},
													},
												},
											},
											"status": {
												Description: "XEKSStatus defines the observed state of XEKS.",
												Type:        "object",
											},
										},
										Required: []string{"spec"},
									}),
								},
							},
						},
					},
				},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newXRD([]byte(tc.inputYAML), tc.customPlural)

			// Compare error messages as strings, trimming whitespace for safety
			gotErrMsg := ""
			wantErrMsg := ""

			if err != nil {
				gotErrMsg = strings.TrimSpace(err.Error())
			}
			if tc.want.err != nil {
				wantErrMsg = strings.TrimSpace(tc.want.err.Error())
			}

			if gotErrMsg != wantErrMsg {
				t.Errorf("newXRD() error - got: %q, want: %q", gotErrMsg, wantErrMsg)
			}

			// Compare the output XRD (ignoring "Required" field for simplicity)
			if diff := cmp.Diff(got, tc.want.xrd, cmpopts.IgnoreFields(extv1.JSONSchemaProps{}, "Required")); diff != "" {
				t.Errorf("newXRD() -got, +want:\n%s", diff)
			}
		})
	}
}

// helper function to convert JSONSchemaProps to RawExtension
func jsonSchemaPropsToRawExtension(schema *extv1.JSONSchemaProps) runtime.RawExtension {
	schemaBytes, _ := json.Marshal(schema)
	return runtime.RawExtension{Raw: schemaBytes}
}
