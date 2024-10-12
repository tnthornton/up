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

package example

import (
	_ "embed"
	"testing"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Embed an XRD YAML file
//
//go:embed testdata/xeks-xrd-definition.yaml
var xeksXRDYAML []byte

func TestCreateResource(t *testing.T) {
	type want struct {
		res resource
		err bool
	}

	cases := map[string]struct {
		resourceType  string
		compositeName string
		apiGroup      string
		apiVersion    string
		name          string
		namespace     string
		want          want
	}{
		"ValidXRCResource": {
			resourceType:  "xrc",
			compositeName: "Cluster",
			apiGroup:      "customer.upbound.io",
			apiVersion:    "v1alpha1",
			name:          "cluster",
			namespace:     "default",
			want: want{
				res: resource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "customer.upbound.io/v1alpha1",
						Kind:       "Cluster",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster",
						Namespace: "default",
					},
					Spec: map[string]interface{}{},
				},
			},
		},
		"ValidXRResource": {
			resourceType:  "xr",
			compositeName: "XCluster",
			apiGroup:      "customer.upbound.io",
			apiVersion:    "v1alpha1",
			name:          "cluster",
			namespace:     "",
			want: want{
				res: resource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "customer.upbound.io/v1alpha1",
						Kind:       "XCluster",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "cluster",
					},
					Spec: map[string]interface{}{},
				},
			},
		},
		"EmptyCompositeName": {
			resourceType:  "xrc",
			compositeName: "",
			apiGroup:      "customer.upbound.io",
			apiVersion:    "v1alpha1",
			name:          "cluster",
			namespace:     "default",
			want: want{
				res: resource{},
				err: true,
			},
		},
		"EmptyAPIGroup": {
			resourceType:  "xrc",
			compositeName: "Cluster",
			apiGroup:      "",
			apiVersion:    "v1alpha1",
			name:          "cluster",
			namespace:     "default",
			want: want{
				res: resource{},
				err: true,
			},
		},
		"EmptyResourceType": {
			resourceType:  "",
			compositeName: "Cluster",
			apiGroup:      "customer.upbound.io",
			apiVersion:    "v1alpha1",
			name:          "cluster",
			namespace:     "default",
			want: want{
				res: resource{},
				err: true,
			},
		},
		"InvalidAPIVersion": {
			resourceType:  "xrc",
			compositeName: "Cluster",
			apiGroup:      "customer.upbound.io",
			apiVersion:    "invalid-version",
			name:          "cluster",
			namespace:     "default",
			want: want{
				res: resource{},
				err: true,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := &generateCmd{}
			got, err := cmd.createResource(tc.resourceType, tc.compositeName, tc.apiGroup, tc.apiVersion, tc.name, tc.namespace)

			// Check if an error was expected and occurred
			if tc.want.err {
				if err == nil {
					t.Errorf("Expected an error but got none for test case %s", name)
				}
				return // Skip further checks if we expected an error
			}

			// Ensure no unexpected error occurred
			if err != nil {
				t.Errorf("Unexpected error for test case %s: %v", name, err)
			}

			// Compare the output resource
			if diff := cmp.Diff(got, tc.want.res); diff != "" {
				t.Errorf("createResource() -got, +want:\n%s", diff)
			}
		})
	}
}

func TestCreateCRDAndGenerateResource(t *testing.T) {
	type want struct {
		crd apiextensionsv1.CustomResourceDefinition
		res resource
		err string
	}

	// Unmarshal the embedded XRD YAML into a CompositeResourceDefinition object
	var xrd v1.CompositeResourceDefinition
	err := yaml.Unmarshal(xeksXRDYAML, &xrd)
	assert.NilError(t, err, "Failed to unmarshal sample XRD")

	trueVar := true
	cases := map[string]struct {
		resourceType string
		want         want
	}{
		"XRCGeneration": {
			resourceType: "xrc",
			want: want{
				err: "cannot derive composite CRD from XRD",
			},
		},
		"XRGeneration": {
			resourceType: "xr",
			want: want{
				crd: apiextensionsv1.CustomResourceDefinition{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "apiextensions.k8s.io/v1",
						Kind:       "CustomResourceDefinition",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks.aws.platform.upbound.io",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "apiextensions.crossplane.io/v1",
								Kind:               "CompositeResourceDefinition",
								Name:               "xeks.aws.platform.upbound.io",
								UID:                "placeholder-uid",
								Controller:         &trueVar,
								BlockOwnerDeletion: &trueVar,
							},
						},
					},
					Spec: apiextensionsv1.CustomResourceDefinitionSpec{
						Group: "aws.platform.upbound.io",
						Names: apiextensionsv1.CustomResourceDefinitionNames{
							Kind:   "XEKS",
							Plural: "xeks",
						},
					},
				},
				res: resource{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "aws.platform.upbound.io/v1alpha1",
						Kind:       "XEKS",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "xeks",
					},
					Spec: map[string]interface{}{
						"parameters": map[string]interface{}{
							"deletionPolicy": "Delete",
							"id":             "string",
							"nodes": map[string]interface{}{
								"count":        float64(1),
								"instanceType": "t3.small",
							},
							"providerConfigName": "default",
							"region":             "string",
						},
					},
				},
				err: "",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := &generateCmd{Type: tc.resourceType}

			gotCRD, err := cmd.createCRDFromXRD(xrd)

			if tc.want.err != "" {
				assert.ErrorContains(t, err, tc.want.err)
				return
			}

			assert.NilError(t, err, "Failed to create CRD from XRD")

			assert.DeepEqual(t, gotCRD, &tc.want.crd, cmp.FilterPath(func(p cmp.Path) bool {
				return p.String() == "Spec"
			}, cmp.Ignore()))

			gotRes, err := cmd.generateResourceFromCRD(gotCRD)
			assert.NilError(t, err, "Failed to generate resource from CRD")

			assert.DeepEqual(t, gotRes, tc.want.res)
		})
	}
}
