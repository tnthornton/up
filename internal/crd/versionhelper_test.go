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
	"testing"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestGetCRDVersion(t *testing.T) {
	type want struct {
		version string
		err     string
	}

	cases := map[string]struct {
		input apiextensionsv1.CustomResourceDefinition
		want  want
	}{
		"CRDWithValidServedAndStorageVersion": {
			input: apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{Name: "v1", Served: true, Storage: true},
						{Name: "v2", Served: true, Storage: false},
					},
				},
			},
			want: want{
				version: "v1",
			},
		},
		"CRDWithNoServedAndStorageVersion": {
			input: apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{Name: "v1", Served: true, Storage: false},
						{Name: "v2", Served: false, Storage: false},
					},
				},
			},
			want: want{
				err: "no served and storage version found in CustomResourceDefinition",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := GetCRDVersion(tc.input)

			if diff := cmp.Diff(got, tc.want.version); diff != "" {
				t.Errorf("GetCRDVersion() version: -got, +want:\n%s", diff)
			}

			if err != nil || tc.want.err != "" {
				if diff := cmp.Diff(err.Error(), tc.want.err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("GetCRDVersion() error: -got, +want:\n%s", diff)
				}
			}
		})
	}
}

func TestGetXRDVersion(t *testing.T) {
	type want struct {
		version string
		err     string
	}

	cases := map[string]struct {
		input v1.CompositeResourceDefinition
		want  want
	}{
		"XRDWithValidReferenceableVersion": {
			input: v1.CompositeResourceDefinition{
				Spec: v1.CompositeResourceDefinitionSpec{
					Versions: []v1.CompositeResourceDefinitionVersion{
						{Name: "v1", Served: true, Referenceable: true},
						{Name: "v2", Served: true, Referenceable: false},
					},
				},
			},
			want: want{
				version: "v1",
			},
		},
		"XRDWithNoReferenceableVersion": {
			input: v1.CompositeResourceDefinition{
				Spec: v1.CompositeResourceDefinitionSpec{
					Versions: []v1.CompositeResourceDefinitionVersion{
						{Name: "v1", Served: true, Referenceable: false},
						{Name: "v2", Served: false, Referenceable: false},
					},
				},
			},
			want: want{
				err: "no referenceable version found in CompositeResourceDefinition",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := GetXRDVersion(tc.input)

			if diff := cmp.Diff(got, tc.want.version); diff != "" {
				t.Errorf("GetXRDVersion() version: -got, +want:\n%s", diff)
			}

			if err != nil || tc.want.err != "" {
				if diff := cmp.Diff(err.Error(), tc.want.err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("GetXRDVersion() error: -got, +want:\n%s", diff)
				}
			}
		})
	}
}
