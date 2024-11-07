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
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// Marshal uses the Kubernetes yaml library to marshal the given object to YAML,
// first removing the metadata.creationTimestamp field if it is present and
// null. An error will be returned if the object is not Kubernetes-like (i.e.,
// it must have metadata).
func Marshal(obj any, opts ...MarshalOption) ([]byte, error) {
	cfg := defaultMarshalOptions
	for _, opt := range opts {
		opt(&cfg)
	}

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

	for _, field := range cfg.removeFields {
		unstructured.RemoveNestedField(unst, field...)
	}
	for _, field := range cfg.removeNilFields {
		val, found, err := unstructured.NestedFieldNoCopy(unst, field...)
		if err != nil {
			return nil, err
		}
		if found && val == nil {
			unstructured.RemoveNestedField(unst, field...)
		}
	}

	return yaml.Marshal(unst)
}

// MarshalOption is an option to influence the behavior of Marshal.
type MarshalOption func(*marshalOptions)

type marshalOptions struct {
	removeFields    []fieldPath
	removeNilFields []fieldPath
}

type fieldPath []string

var defaultMarshalOptions = marshalOptions{
	removeNilFields: []fieldPath{
		{"metadata", "creationTimestamp"},
	},
}

// RemoveField causes Marshal to remove the given field from the object before
// marshaling. Field paths are dot-separated without a leading dot.
func RemoveField(path string) MarshalOption {
	return func(opts *marshalOptions) {
		fieldPath := strings.Split(path, ".")
		opts.removeFields = append(opts.removeFields, fieldPath)
	}
}

// RemoveFieldIfNil causes Marshal to remove the given field from the object
// before marshaling if its value is nil. Field paths are dot-separated without
// a leading dot.
func RemoveFieldIfNil(path string) MarshalOption {
	return func(opts *marshalOptions) {
		fieldPath := strings.Split(path, ".")
		opts.removeNilFields = append(opts.removeNilFields, fieldPath)
	}
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
