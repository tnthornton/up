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
	"errors"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// GetCRDVersion iterates over the versions defined in the CustomResourceDefinition (CRD).
// It returns the name of the version that has both "served" and "storage" fields set to true.
func GetCRDVersion(crd apiextensionsv1.CustomResourceDefinition) (string, error) {
	for _, version := range crd.Spec.Versions {
		if version.Served && version.Storage {
			return version.Name, nil
		}
	}
	return "", errors.New("no served and storage version found in CustomResourceDefinition")
}

// GetXRDVersion iterates over the versions defined in the CompositeResourceDefinition (XRD).
// It returns the name of the version that has both "served" and "referenceable" fields set to true.
func GetXRDVersion(xrd v1.CompositeResourceDefinition) (string, error) {
	for _, version := range xrd.Spec.Versions {
		if version.Served && version.Referenceable {
			return version.Name, nil
		}
	}
	return "", errors.New("no referenceable version found in CompositeResourceDefinition")
}
