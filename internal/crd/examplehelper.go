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
	"regexp"
	"slices"
	"sort"
	"strconv"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const array = "array"

var RootRequiredFields = []string{"apiVersion", "kind", "spec", "metadata"}

// GenerateExample creates an example manifest for a given CRD, optionally minimizing fields and skipping random value generation.
func GenerateExample(crd apiextensionsv1.CustomResourceDefinition, minimal, skipRandom bool) (map[string]interface{}, error) {
	parser := newParser(crd.Spec.Group, crd.Spec.Names.Kind, minimal, skipRandom)

	version, err := GetCRDVersion(crd)
	if err != nil {
		return nil, errors.Wrapf(err, "version not found")
	}

	for _, ver := range crd.Spec.Versions {
		if ver.Name == version {
			yamlData, err := parser.parseProperties(ver.Name, ver.Schema.OpenAPIV3Schema.Properties, RootRequiredFields)
			if err != nil {
				return nil, fmt.Errorf("failed to parse properties: %w", err)
			}
			return yamlData, nil
		}
	}

	return nil, nil
}

// parser struct remains the same.
type parser struct {
	inArray      bool
	indent       int
	group        string
	kind         string
	onlyRequired bool
	skipRandom   bool
}

// newParser creates a new parser.
func newParser(group, kind string, requiredOnly, skipRandom bool) *parser {
	return &parser{
		group:        group,
		kind:         kind,
		onlyRequired: requiredOnly,
		skipRandom:   skipRandom,
	}
}

// parseProperties generates a map of properties.
func (p *parser) parseProperties(version string, properties map[string]apiextensionsv1.JSONSchemaProps, requiredFields []string) (map[string]interface{}, error) { // nolint:gocyclo
	result := make(map[string]interface{})

	// Sort the property keys
	sortedKeys := make([]string, 0, len(properties))
	for k := range properties {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, k := range sortedKeys {
		// If field is not required and only handling required fields, skip it.
		if p.onlyRequired && !slices.Contains(requiredFields, k) {
			continue
		}

		prop := properties[k]

		switch {
		// handle simple properties (no sub-properties or additionalProperties)
		case len(prop.Properties) == 0 && prop.AdditionalProperties == nil:
			var value interface{}
			if k == "apiVersion" { // nolint:gocritic
				value = fmt.Sprintf("%s/%s", p.group, version)
			} else if k == "kind" && p.indent == 0 {
				value = p.kind
			} else if prop.Type == array && prop.Items != nil && prop.Items.Schema != nil && len(prop.Items.Schema.Properties) > 0 {
				// handle array of objects
				p.inArray = true
				subProperties, err := p.parseProperties(version, prop.Items.Schema.Properties, prop.Items.Schema.Required)
				if err != nil {
					return nil, err
				}
				value = []interface{}{subProperties}
			} else {
				// generate sample value based on type
				value = outputValueType(prop, p.skipRandom)
			}
			result[k] = value

		// handle sub-properties (recursion)
		case len(prop.Properties) > 0:
			// if this property is not required, do not include its sub-properties
			if p.onlyRequired && !slices.Contains(requiredFields, k) {
				continue
			}

			subProperties, err := p.parseProperties(version, prop.Properties, prop.Required)
			if err != nil {
				return nil, err
			}
			result[k] = subProperties

		// handle additionalProperties (e.g., free-form objects)
		case prop.AdditionalProperties != nil:
			result[k] = make(map[string]interface{})
		}
	}

	return result, nil
}

// outputValueType generates an output value based on the given type.
func outputValueType(v apiextensionsv1.JSONSchemaProps, skipRandom bool) interface{} { // nolint:gocyclo
	if v.Default != nil {
		var defaultValue interface{}
		err := yaml.Unmarshal(v.Default.Raw, &defaultValue)
		if err == nil {
			return defaultValue
		}
	}

	if v.Example != nil {
		var exampleValue interface{}
		err := yaml.Unmarshal(v.Example.Raw, &exampleValue)
		if err == nil {
			return exampleValue
		}
	}

	if v.Pattern != "" && !skipRandom {
		// If it's a valid regex, return a value that matches the regex
		if _, err := regexp.Compile(v.Pattern); err == nil {
			return gofakeit.Regex(v.Pattern)
		}
	}

	if v.Enum != nil {
		var enumValue interface{}
		err := yaml.Unmarshal(v.Enum[0].Raw, &enumValue)
		if err == nil {
			return enumValue
		}
	}

	switch v.Type {
	case "string":
		return "string"
	case "integer":
		if v.Minimum != nil {
			return strconv.Itoa(int(*v.Minimum))
		}
		return 1
	case "boolean":
		return true
	case "object":
		return map[string]interface{}{}
	case array:
		if v.Items.Schema != nil {
			return []interface{}{v.Items.Schema.Type}
		}
	}

	return nil
}
