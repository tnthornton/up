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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pterm/pterm"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/pkg/errors"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/xcrd"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	exampleCrd "github.com/upbound/up/internal/crd"
)

const (
	outputFile = "file"
	outputYAML = "yaml"
	outputJSON = "json"
	xr         = "Composite Resource (XR)"
	xrc        = "Composite Resource Claim (XRC)"
)

var (
	crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
)

type resource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              map[string]interface{} `json:"spec"`
}

type generateCmd struct {
	Path   string `help:"Specifies the path to the output file where the  Composite Resource (XR) or Composite Resource Claim (XRC) will be saved." optional:""`
	Output string `help:"Specifies the output format for the results. Use 'file' to save to a file, 'yaml' to display the  Composite Resource (XR) or Composite Resource Claim (XRC) in YAML format, or 'json' to display in JSON format." short:"o" default:"file" enum:"file,yaml,json"`

	Type       string `help:"Specifies the type of resource to create: 'xrc' for Composite Resource Claim (XRC), 'xr' for Composite Resource (XR)." default:"" enum:"xr,xrc,claim,"`
	APIGroup   string `help:"Specifies the API group for the resource."`
	APIVersion string `help:"Specifies the API version for the resource."`
	Kind       string `help:"Specifies the Kind of the resource."`

	XRDFilePath string `arg:"" optional:"" help:"Specifies the path to the Composite Resource Definition (XRD) file used to generate an example resource."`
}

func (c *generateCmd) Run(ctx context.Context) error {
	// get xr or xrc/claim as input otherwise ask interactive
	if c.Type == "" {
		c.Type = c.getInteractiveType()
	}
	if len(c.XRDFilePath) > 0 {
		return c.processXRDFile()
	}
	return c.processInput()
}

// processXRDFile handles the logic when the XRD file path is provided
func (c *generateCmd) processXRDFile() error {
	xrd, err := c.readXRDFile()
	if err != nil {
		return err
	}

	crd, err := c.createCRDFromXRD(xrd)
	if err != nil {
		return err
	}

	resource, err := c.generateResourceFromCRD(crd)
	if err != nil {
		return err
	}

	return c.outputResource(resource)
}

// readXRDFile reads and unmarshals the XRD file
func (c *generateCmd) readXRDFile() (v1.CompositeResourceDefinition, error) {
	var xrd v1.CompositeResourceDefinition

	xrdRaw, err := os.ReadFile(c.XRDFilePath)
	if err != nil {
		return xrd, errors.Wrapf(err, "failed to read XRD file at %s", c.XRDFilePath)
	}

	err = yaml.Unmarshal(xrdRaw, &xrd)
	if err != nil {
		return xrd, errors.Wrapf(err, "failed to unmarshal XRD file")
	}

	return xrd, nil
}

// createCRDFromXRD creates a CRD from the XRD
func (c *generateCmd) createCRDFromXRD(xrd v1.CompositeResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	var crd *apiextensionsv1.CustomResourceDefinition
	var err error

	if c.Type == "xrc" || c.Type == "claim" {
		crd, err = xcrd.ForCompositeResourceClaim(&xrd)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q for Composite Resource Claim", xrd.GetName())
		}
	} else if c.Type == "xr" {
		crd, err = xcrd.ForCompositeResource(&xrd)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q for Composite Resource", xrd.GetName())
		}
	}

	crd.SetGroupVersionKind(crdGVK)
	return crd, nil
}

// generateResourceFromCRD generates a resource from a CRD
func (c *generateCmd) generateResourceFromCRD(crd *apiextensionsv1.CustomResourceDefinition) (resource, error) {
	var res resource

	yamlData, err := exampleCrd.GenerateExample(*crd, true, false)
	if err != nil {
		return res, errors.Wrapf(err, "failed generating example")
	}

	yamlBytes, err := yaml.Marshal(&yamlData)
	if err != nil {
		return res, errors.Wrapf(err, "failed to marshal generated yaml")
	}

	err = yaml.Unmarshal(yamlBytes, &res)
	if err != nil {
		return res, errors.Wrapf(err, "failed to unmarshal generated schema")
	}

	res.ObjectMeta.Name = fmt.Sprintf("example-%s", strings.ToLower(res.Kind))
	if c.Type == "xrc" || c.Type == "claim" {
		res.ObjectMeta.Namespace = "default"
	}

	return res, nil
}

// processInput handles the logic when the XRD file path is not provided (interactive input)
func (c *generateCmd) processInput() error {
	resourceType, compositeName, apiGroup, apiVersion, err := c.collectInteractiveInput()
	if err != nil {
		return err
	}

	resource := c.createResource(resourceType, compositeName, apiGroup, apiVersion)
	return c.outputResource(resource)
}

// collectInteractiveInput collects user input interactively
func (c *generateCmd) collectInteractiveInput() (string, string, string, string, error) {
	return c.getInteractiveType(), c.getInteractiveName(c.Type), c.getInteractiveGroup(), c.getInteractiveVersion(), nil
}

// getInteractiveType gets the resource type interactively
func (c *generateCmd) getInteractiveType() string {
	if c.Type != "" {
		return c.Type
	}

	confirm := pterm.DefaultInteractiveSelect.
		WithOptions([]string{xrc, xr}).
		WithDefaultOption(xrc).
		WithDefaultText("What do you want to create?")

	choice, err := confirm.Show()
	if err != nil {
		pterm.Error.Println("An error occurred while getting choice:", err)
		return ""
	}

	var cType string
	if choice == xrc {
		cType = "xrc"
	}

	if choice == xr {
		cType = "xr"
	}

	return cType
}

// getInteractiveName gets the resource name interactively
func (c *generateCmd) getInteractiveName(resourceType string) string {
	if c.Kind != "" {
		return c.Kind
	}

	var input pterm.InteractiveTextInputPrinter
	if resourceType == "xrc" {
		input = *pterm.DefaultInteractiveTextInput.
			WithDefaultText("What is your Composite Resource Claim (XRC) named?").
			WithDefaultValue("Cluster")
	} else {
		input = *pterm.DefaultInteractiveTextInput.
			WithDefaultText("What is your Composite Resource (XR) named?").
			WithDefaultValue("XCluster")
	}

	name, err := input.Show()
	if err != nil {
		pterm.Error.Println("An error occurred while getting Claim or Composite Resource name:", err)
		return ""
	}

	return name
}

// getInteractiveGroup gets the API group interactively
func (c *generateCmd) getInteractiveGroup() string {
	if c.APIGroup != "" {
		return c.APIGroup
	}

	input := pterm.DefaultInteractiveTextInput.
		WithDefaultText("What is the API group named?").
		WithDefaultValue("customer.upbound.io")

	group, err := input.Show()
	if err != nil {
		pterm.Error.Println("An error occurred while getting API Group:", err)
		return ""
	}

	return group
}

// getInteractiveVersion gets the API version interactively
func (c *generateCmd) getInteractiveVersion() string {
	if c.APIVersion != "" {
		return c.APIVersion
	}

	input := pterm.DefaultInteractiveTextInput.
		WithDefaultText("What is the API Version named?").
		WithDefaultValue("v1alpha1")

	version, err := input.Show()
	if err != nil {
		pterm.Error.Println("An error occurred while getting API Version:", err)
		return ""
	}

	return version
}

// createResource creates a resource based on the collected input
func (c *generateCmd) createResource(resourceType, compositeName, apiGroup, apiVersion string) resource {
	res := resource{
		TypeMeta: metav1.TypeMeta{
			APIVersion: fmt.Sprintf("%s/%s", apiGroup, apiVersion),
			Kind:       compositeName,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("example-%s", strings.ToLower(compositeName)),
		},
		Spec: map[string]interface{}{},
	}

	if resourceType == "xrc" || resourceType == "claim" {
		res.ObjectMeta.Namespace = "default"
	}

	return res
}

// outputResource handles the output of the generated resource based on the specified output type
func (c *generateCmd) outputResource(res resource) error {
	// Clean up resource to remove unnecessary fields
	resourceClean := map[string]interface{}{
		"apiVersion": res.APIVersion,
		"kind":       res.Kind,
		"metadata": map[string]interface{}{
			"name": res.ObjectMeta.Name,
		},
		"spec": res.Spec,
	}

	if len(res.ObjectMeta.Namespace) > 0 {
		resourceClean["metadata"].(map[string]interface{})["namespace"] = res.ObjectMeta.Namespace
	}

	// Convert resource to YAML format
	resourceYAML, err := yaml.Marshal(resourceClean)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal resource to YAML")
	}

	switch c.Output {
	case outputFile:
		filePath := c.Path
		if filePath == "" {
			filePath = fmt.Sprintf("examples/%s/example-%s.yaml", strings.ToLower(res.Kind), strings.ToLower(res.Kind))
		}

		outputDir := filepath.Dir(filepath.Clean(filePath))
		if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "failed to create output directory")
		}

		if err := os.WriteFile(filePath, resourceYAML, 0644); err != nil { // nolint:gosec // writing to file
			return errors.Wrapf(err, "failed to write resource to file")
		}

		pterm.Printfln("Successfully created resource and saved to %s", filePath)
	case outputYAML:
		pterm.Printfln(string(resourceYAML))
	case outputJSON:
		jsonData, err := yaml.YAMLToJSON(resourceYAML)
		if err != nil {
			return errors.Wrapf(err, "failed to convert resource to JSON")
		}
		pterm.Printfln(string(jsonData))
	default:
		return errors.New("invalid output format specified")
	}

	return nil
}
