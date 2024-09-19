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

package crd

import (
	_ "embed"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

//go:embed testdata/template.fn.crossplane.io_kclinputs.yaml
var crdYAML []byte

// TestGenerate tests the Generate function using a CRD loaded from an embedded YAML file.
func TestGenerate(t *testing.T) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(crdYAML, &crd); err != nil {
		t.Fatalf("failed to unmarshal embedded CRD: %v", err)
	}

	tests := map[string]struct {
		crd        *apiextensionsv1.CustomResourceDefinition
		minimal    bool
		skipRandom bool
		want       map[string]interface{}
		wantErr    bool
	}{
		"GetExampleFromCRDWithRequiredFields": {
			crd:        &crd,
			minimal:    true,
			skipRandom: false,
			want: map[string]interface{}{
				"apiVersion": "template.fn.crossplane.io/v1beta1",
				"kind":       "KCLInput",
				"metadata":   map[string]interface{}{},
				"spec": map[string]interface{}{
					"source": "string",
					"target": "Resources",
				},
			},
			wantErr: false,
		},
		"GetFullExample": {
			crd:        &crd,
			minimal:    false,
			skipRandom: false,
			want: map[string]interface{}{
				"apiVersion": "template.fn.crossplane.io/v1beta1",
				"kind":       "KCLInput",
				"metadata":   map[string]interface{}{},
				"spec": map[string]interface{}{
					"config": map[string]interface{}{
						"arguments":        []interface{}{"string"},
						"debug":            true,
						"disableNone":      true,
						"overrides":        []interface{}{"string"},
						"pathSelectors":    []interface{}{"string"},
						"settings":         []interface{}{"string"},
						"showHidden":       true,
						"sortKeys":         true,
						"strictRangeCheck": true,
						"vendor":           true,
					},
					"credentials": map[string]interface{}{
						"password": "string",
						"url":      "string",
						"username": "string",
					},
					"dependencies": "string",
					"params":       map[string]interface{}{},
					"resources":    []interface{}{map[string]interface{}{"base": map[string]interface{}{}, "name": "string"}},
					"source":       "string",
					"target":       "Resources",
				},
			},
			wantErr: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := GenerateExample(*tc.crd, tc.minimal, tc.skipRandom)
			if (err != nil) != tc.wantErr {
				t.Errorf("Generate() error = %v, wantErr %v", err, tc.wantErr)
				return
			}

			if diff := cmp.Diff(got, tc.want, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Generate(): -got +want:\n%s", diff)
			}
		})
	}
}

func TestParseProperties(t *testing.T) {
	type args struct {
		version        string
		properties     map[string]apiextensionsv1.JSONSchemaProps
		requiredFields []string
	}
	tests := map[string]struct {
		parser  *parser
		args    args
		want    map[string]interface{}
		wantErr bool
	}{
		"SimpleProperties": {
			parser: &parser{group: "example.com", kind: "Example", indent: 0, skipRandom: true},
			args: args{
				version: "v1",
				properties: map[string]apiextensionsv1.JSONSchemaProps{
					"apiVersion": {Type: "string"},
					"kind":       {Type: "string"},
					"metadata": {
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"name": {Type: "string"},
						},
					},
				},
				requiredFields: []string{"apiVersion", "kind", "metadata"},
			},
			want: map[string]interface{}{
				"apiVersion": "example.com/v1",
				"kind":       "Example",
				"metadata": map[string]interface{}{
					"name": "string",
				},
			},
			wantErr: false,
		},
		"RequiredFieldsOnly": {
			parser: &parser{group: "example.com", kind: "Example", indent: 0, onlyRequired: true, skipRandom: true},
			args: args{
				version: "v1",
				properties: map[string]apiextensionsv1.JSONSchemaProps{
					"apiVersion": {Type: "string"},
					"kind":       {Type: "string"},
					"spec": {
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"replicas": {Type: "integer"},
							"selector": {Type: "object"},
						},
						Required: []string{"replicas"},
					},
				},
				requiredFields: []string{"apiVersion", "kind", "spec"},
			},
			want: map[string]interface{}{
				"apiVersion": "example.com/v1",
				"kind":       "Example",
				"spec": map[string]interface{}{
					"replicas": 1,
				},
			},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tt.parser.parseProperties(tt.args.version, tt.args.properties, tt.args.requiredFields)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseProperties() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("ParseProperties() mismatch (-got +want):\n%s", diff)
			}
		})
	}
}

func TestOutputValueType(t *testing.T) {
	cases := map[string]struct {
		input      apiextensionsv1.JSONSchemaProps
		skipRandom bool
		want       interface{}
	}{
		"DefaultValueProvided": {
			input: apiextensionsv1.JSONSchemaProps{
				Default: &apiextensionsv1.JSON{Raw: []byte(`"default_value"`)},
			},
			want: "default_value",
		},
		"ExampleProvided": {
			input: apiextensionsv1.JSONSchemaProps{
				Example: &apiextensionsv1.JSON{Raw: []byte(`"example_value"`)},
			},
			want: "example_value",
		},
		"EnumProvided": {
			input: apiextensionsv1.JSONSchemaProps{
				Enum: []apiextensionsv1.JSON{
					{Raw: []byte(`"enum_value"`)},
				},
			},
			want: "enum_value",
		},
		"TypeString": {
			input: apiextensionsv1.JSONSchemaProps{
				Type: "string",
			},
			want: "string",
		},
		"TypeIntegerWithMinimum": {
			input: apiextensionsv1.JSONSchemaProps{
				Type:    "integer",
				Minimum: func(i float64) *float64 { return &i }(5),
			},
			want: "5",
		},
		"TypeIntegerWithoutMinimum": {
			input: apiextensionsv1.JSONSchemaProps{
				Type: "integer",
			},
			want: 1,
		},
		"TypeBoolean": {
			input: apiextensionsv1.JSONSchemaProps{
				Type: "boolean",
			},
			want: true,
		},
		"TypeObject": {
			input: apiextensionsv1.JSONSchemaProps{
				Type: "object",
			},
			want: map[string]interface{}{},
		},
		"TypeArray": {
			input: apiextensionsv1.JSONSchemaProps{
				Type: "array",
				Items: &apiextensionsv1.JSONSchemaPropsOrArray{
					Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "string",
					},
				},
			},
			want: []interface{}{"string"},
		},
		"InvalidPatternSkipRandomTrue": {
			input: apiextensionsv1.JSONSchemaProps{
				Pattern: "[",
			},
			skipRandom: true,
			want:       nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := outputValueType(tc.input, tc.skipRandom)

			if diff := cmp.Diff(got, tc.want, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("outputValueType(): -got, +want:\n%s", diff)
			}
		})
	}
}
