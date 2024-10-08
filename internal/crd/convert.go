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
	"fmt"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/spf13/afero"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"sigs.k8s.io/yaml"
)

func ConvertToOpenAPI(fs afero.Fs, bs []byte, path, baseFolder string) (string, error) {
	var crd extv1.CustomResourceDefinition
	if err := yaml.Unmarshal(bs, &crd); err != nil {
		return "", errors.Wrapf(err, "failed to unmarshal CRD file %q", path)
	}

	version, err := GetCRDVersion(crd)
	if err != nil {
		return "", err
	}

	// Generate OpenAPI v3 schema for the latest version
	openAPIRaw, err := builder.BuildOpenAPIV3(&crd, version, builder.Options{}) // todo
	if err != nil {
		return "", fmt.Errorf("failed to build OpenAPI v3 schema: %w", err)
	}

	// Marshal the output to YAML
	openAPIBytes, err := yaml.Marshal(openAPIRaw)
	if err != nil {
		return "", fmt.Errorf("failed to marshal OpenAPI output to YAML: %w", err)
	}

	// Define the output path for the OpenAPI schema file
	groupFormatted := strings.ReplaceAll(crd.Spec.Group, ".", "_")
	kindFormatted := strings.ToLower(crd.Spec.Names.Kind)
	openAPIPath := fmt.Sprintf("%s_%s_%s.yaml", groupFormatted, version, kindFormatted)

	// Write the output to a file
	if err := afero.WriteFile(fs, openAPIPath, openAPIBytes, 0o644); err != nil {
		return "", fmt.Errorf("failed to write OpenAPI file: %w", err)
	}

	return openAPIPath, nil
}
