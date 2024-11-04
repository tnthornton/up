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

package yaml

import (
	"reflect"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// Marshal uses the Kubernetes yaml library to marshal the given object to YAML,
// first removing the metadata.creationTimestamp field if it is present and
// null. An error will be returned if the object is not Kubernetes-like (i.e.,
// it must have metadata).
func Marshal(obj any) ([]byte, error) {
	// Only pointers can be converted to unstructured.
	typ := reflect.TypeOf(obj)
	if typ.Kind() != reflect.Pointer {
		// Have to use ptr.To here instead of just taking the address because
		// obj is a stack variable.
		obj = ptr.To(obj)
	}

	unst, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	// Remove metadata.creationTimestamp if it's nil. Leave it in place if it
	// has any other value.
	meta, hasMeta := unst["metadata"].(map[string]interface{})
	if hasMeta {
		ctime, hasCTime := meta["creationTimestamp"]
		if hasCTime && ctime == nil {
			delete(meta, "creationTimestamp")
		}
	}

	return yaml.Marshal(unst)
}

// Unmarshal wraps the Kubernetes yaml package's Unmarshal, allowing this
// package to serve as a drop-in replacement for the upstream package in most
// cases.
func Unmarshal(y []byte, obj any, opts ...yaml.JSONOpt) error {
	return yaml.Unmarshal(y, obj, opts...)
}

// YAMLToJSON wraps the Kubernetes yaml package's YAMLToJSON, allowing this
// package to serve as a drop-in replacement for the upstream package in most
// cases.
func YAMLToJSON(y []byte) ([]byte, error) {
	return yaml.YAMLToJSON(y)
}
