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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/gobuffalo/flect"
	"github.com/pterm/pterm"
	"sigs.k8s.io/yaml"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	outputFile = "file"
	outputYAML = "yaml"
	outputJSON = "json"
)

type inputYAML struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              map[string]interface{} `json:"spec"`
	Status            map[string]interface{} `json:"status"`
}

type generateCmd struct {
	File   string `arg:"" help:"Path to the file containing the Composite Resource (XR) or Composite Resource Claim (XRC)."`
	Path   string `help:"Path to the output file where the Composite Resource Definition (XRD) will be saved." optional:""`
	Plural string `help:"Optional custom plural form for the Composite Resource Definition (XRD)." optional:""`
	Output string `help:"Output format for the results: 'file' to save to a file, 'yaml' to print XRD in YAML format, 'json' to print XRD in JSON format." short:"o" default:"file" enum:"file,yaml,json"`
}

func (c *generateCmd) Run(ctx context.Context, p pterm.TextPrinter) error { // nolint:gocyclo

	yamlData, err := os.ReadFile(c.File)
	if err != nil {
		return errors.Wrapf(err, "Failed to read input file")
	}

	xrd, err := newXRD(yamlData, c.Plural)
	if err != nil {
		return errors.Wrapf(err, "Failed to create CompositeResourceDefinition")
	}

	// get rid of metadata.creationTimestamp nil
	// get rid of status
	xrdClean := map[string]interface{}{
		"apiVersion": xrd.APIVersion,
		"kind":       xrd.Kind,
		"metadata": map[string]interface{}{
			"name": xrd.ObjectMeta.Name,
		},
		"spec": xrd.Spec,
	}

	// Convert XRD to YAML format
	xrdYAML, err := yaml.Marshal(xrdClean)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal XRD to YAML")
	}

	switch c.Output {
	case outputFile:
		// Determine the file path
		filePath := c.Path
		if filePath == "" {
			filePath = fmt.Sprintf("apis/%s/definition.yaml", xrd.Spec.Names.Plural)
		}

		// Ensure the directory exists before writing the file
		outputDir := filepath.Dir(filepath.Clean(filePath))
		if err = os.MkdirAll(outputDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "failed to create output directory")
		}

		// Write the YAML to the specified output file
		if err = os.WriteFile(filePath, xrdYAML, 0644); err != nil { // nolint:gosec // writing to file
			return errors.Wrapf(err, "failed to write XRD to file")
		}

		p.Printfln("Successfully created CompositeResourceDefinition and saved to %s", filePath)

	case outputYAML:
		p.Println(string(xrdYAML))

	case outputJSON:
		jsonData, err := yaml.YAMLToJSON(xrdYAML)
		if err != nil {
			return errors.Wrapf(err, "failed to convert XRD to JSON")
		}
		p.Println(string(jsonData))

	default:
		return errors.New("invalid output format specified")
	}

	return nil
}

// newXRD to create a new CompositeResourceDefinition and fail if inferProperties fails
func newXRD(yamlData []byte, customPlural string) (*v1.CompositeResourceDefinition, error) { // nolint:gocyclo
	var input inputYAML
	err := yaml.Unmarshal(yamlData, &input)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal YAML")
	}

	// Ensure only allowed top-level keys: apiVersion, kind, metadata, spec, and status
	var topLevelKeys map[string]interface{}
	err = yaml.Unmarshal(yamlData, &topLevelKeys)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal YAML to check top-level keys")
	}
	for key := range topLevelKeys {
		if key != "apiVersion" && key != "kind" && key != "metadata" && key != "spec" && key != "status" {
			return nil, errors.New("invalid manifest: only apiVersion, kind, metadata, spec, and status are allowed as top-level keys")
		}
	}

	if input.APIVersion == "" {
		return nil, errors.New("invalid manifest: apiVersion is required")
	}

	// Check if apiVersion contains exactly one slash (/) to ensure it's in "group/version" format
	if strings.Count(input.APIVersion, "/") != 1 {
		return nil, errors.New("invalid manifest: apiVersion must be in the format group/version")
	}

	if input.Kind == "" {
		return nil, errors.New("invalid manifest: kind is required")
	}
	if input.Name == "" {
		return nil, errors.New("invalid manifest: metadata.name is required")
	}
	if input.Spec == nil {
		return nil, errors.New("invalid manifest: spec is required")
	}

	// List of standard Crossplane fields to remove from the XR/XRC.
	// These fields are automatically populated by Crossplane when the CRD is created in the cluster.
	fieldsToRemove := []string{
		"resourceRefs",
		"writeConnectionSecretToRef",
		"publishConnectionDetailsTo",
		"environmentConfigRefs",
		"compositionUpdatePolicy",
		"compositionRevisionRef",
		"compositionRevisionSelector",
		"compositionRef",
		"compositionSelector",
		"claimRef",
	}

	for _, field := range fieldsToRemove {
		delete(input.Spec, field)
	}

	gv, err := schema.ParseGroupVersion(input.APIVersion)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse API version")
	}

	group := gv.Group
	version := gv.Version
	kind := input.Kind

	// Use custom plural if provided, otherwise generate it
	plural := customPlural
	if plural == "" {
		plural = flect.Pluralize(kind)
	}

	description := fmt.Sprintf("%s is the Schema for the %s API.", kind, kind)

	// Infer properties for spec and status and handle errors
	specProps, err := inferProperties(input.Spec)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to infer properties for spec")
	}

	statusProps, err := inferProperties(input.Status)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to infer properties for status")
	}

	openAPIV3Schema := &extv1.JSONSchemaProps{
		Description: description,
		Type:        "object",
		Properties: map[string]extv1.JSONSchemaProps{
			"spec": {
				Description: fmt.Sprintf("%sSpec defines the desired state of %s.", kind, kind),
				Type:        "object",
				Properties:  specProps,
			},
			"status": {
				Description: fmt.Sprintf("%sStatus defines the observed state of %s.", kind, kind),
				Type:        "object",
				Properties:  statusProps,
			},
		},
		Required: []string{"spec"},
	}

	// Convert openAPIV3Schema as JSONSchemaProps to a RawExtension
	schemaBytes, err := json.Marshal(openAPIV3Schema)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to marshal OpenAPI v3 schema")
	}

	rawSchema := &runtime.RawExtension{
		Raw: schemaBytes,
	}

	// Determine whether to modify based on XRC
	if input.Namespace != "" {
		// Ensure plural and kind start with 'x'
		if !strings.HasPrefix(plural, "x") {
			plural = "x" + plural
		}
		if !strings.HasPrefix(kind, "x") {
			kind = "x" + kind
		}
	}

	// Construct the XRD
	xrd := &v1.CompositeResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.CompositeResourceDefinitionGroupVersionKind.GroupVersion().String(),
			Kind:       v1.CompositeResourceDefinitionGroupVersionKind.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.ToLower(fmt.Sprintf("%s.%s", plural, group)),
		},
		Spec: v1.CompositeResourceDefinitionSpec{
			Group: group,
			Names: extv1.CustomResourceDefinitionNames{
				Categories: []string{"crossplane"},
				Kind:       flect.Capitalize(kind),
				Plural:     strings.ToLower(plural),
			},
			Versions: []v1.CompositeResourceDefinitionVersion{
				{
					Name:          version,
					Referenceable: true,
					Served:        true,
					Schema: &v1.CompositeResourceValidation{
						OpenAPIV3Schema: *rawSchema,
					},
				},
			},
		},
	}

	// Conditionally add ClaimNames without 'x' prefix if metadata.namespace is present
	if input.Namespace != "" {
		claimPlural := strings.ToLower(strings.TrimPrefix(plural, "x"))
		claimKind := flect.Capitalize(strings.TrimPrefix(kind, "x"))

		xrd.Spec.ClaimNames = &extv1.CustomResourceDefinitionNames{
			Kind:   claimKind,
			Plural: claimPlural,
		}
	}

	return xrd, nil
}

// inferProperties to return the correct type
func inferProperties(spec map[string]interface{}) (map[string]extv1.JSONSchemaProps, error) {
	properties := make(map[string]extv1.JSONSchemaProps)

	for key, value := range spec {
		strKey := fmt.Sprintf("%v", key)
		inferredProp, err := inferProperty(value)
		if err != nil {
			// Return the error and propagate it upwards
			return nil, errors.Wrapf(err, "error inferring property for key '%s'", strKey)
		}
		properties[strKey] = inferredProp
	}

	return properties, nil
}

// inferProperty to return extv1.JSONSchemaProps
func inferProperty(value interface{}) (extv1.JSONSchemaProps, error) { // nolint:gocyclo
	// Explicitly handle nil
	if value == nil {
		return extv1.JSONSchemaProps{
			Type: "string", // Ensure this returns "string" for nil
		}, nil
	}

	switch v := value.(type) {
	case string:
		return extv1.JSONSchemaProps{
			Type: "string",
		}, nil
	case int, int32, int64:
		return extv1.JSONSchemaProps{
			Type: "integer",
		}, nil
	case float32, float64:
		return extv1.JSONSchemaProps{
			Type: "number",
		}, nil
	case bool:
		return extv1.JSONSchemaProps{
			Type: "boolean",
		}, nil
	case map[string]interface{}:
		// Recursively infer properties for nested objects and handle errors
		inferredProps, err := inferProperties(v)
		if err != nil {
			return extv1.JSONSchemaProps{}, errors.Wrap(err, "error inferring properties for object")
		}
		return extv1.JSONSchemaProps{
			Type:       "object",
			Properties: inferredProps,
		}, nil
	case []interface{}:
		if len(v) > 0 {
			// Infer the type of the first element
			firstElemSchema, err := inferProperty(v[0])
			if err != nil {
				return extv1.JSONSchemaProps{}, err
			}

			// Check if all elements are of the same type
			for _, elem := range v {
				elemSchema, err := inferProperty(elem)
				if err != nil {
					return extv1.JSONSchemaProps{}, err
				}
				if elemSchema.Type != firstElemSchema.Type {
					// Return an error if mixed types are found (remove elemSchema.Items)
					return extv1.JSONSchemaProps{}, errors.New("mixed types detected in array")
				}
			}

			// If all types are the same, return the inferred type
			return extv1.JSONSchemaProps{
				Type: "array",
				Items: &extv1.JSONSchemaPropsOrArray{
					Schema: &firstElemSchema,
				},
			}, nil
		}

		// If the array is empty, default to array of objects
		return extv1.JSONSchemaProps{
			Type: "array",
			Items: &extv1.JSONSchemaPropsOrArray{
				Schema: &extv1.JSONSchemaProps{
					Type: "object",
				},
			},
		}, nil
	default:
		// Return an error for unknown types (excluding nil which is handled earlier)
		return extv1.JSONSchemaProps{}, errors.Errorf("unknown type: %T", value)
	}
}
