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
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/xcrd"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	icrd "github.com/upbound/up/internal/crd"
	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/yaml"
)

func (c *generateCmd) Help() string {
	return `
The 'generate' command is used to create an Composite Resource (XR) or Composite Resource Claim (XRC) resource.

Examples:

    example generate
        Creates an Composite Resource (XR) or Composite Resource Claim (XRC) resource. All necessary inputs will be collected interactively
        and saved in the 'example' project directory.

    example generate --name example --namespace default
        Sets the metadata name and namespace. All other inputs will be collected interactively
        and saved in the 'example' project directory.

    example generate --type claim --api-group acme.comp --api-version v1beta1 --kind Cluster --name example
        Creates a Composite Resource Claim (XRC) with specified api-group, api-version, kind, and metadata name. All additional inputs
        will be collected interactively and saved in the 'example' project directory.

    example generate apis/xnetworks/definition.yaml
        Generates an Composite Resource (XR) or Composite Resource Claim (XRC) from an CompositeResourceDefinition (XRD) definition. Necessary inputs are collected interactively,
        with default values and enums to scaffold a functional skeleton, saved in the 'example' project directory.

    example generate apis/xnetworks/definition.yaml --type xr
        Creates an Composite Resource (XR) from an CompositeResourceDefinition (XRD) definition with default values and enums to scaffold a functional skeleton,
        saved in the 'example' project directory.
`
}

const (
	outputFile = "file"
	outputYAML = "yaml"
	outputJSON = "json"
	xr         = "Composite Resource (XR)"
	xrc        = "Composite Resource Claim (XRC)"
)

var (
	crdGVK        = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
	dnsLabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
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
	Name       string `help:"Specifies the Name of the resource."`
	Namespace  string `help:"Specifies the Namespace of the resource."`

	XRDFilePath    string `arg:"" optional:"" help:"Specifies the path to the Composite Resource Definition (XRD) file used to generate an example resource."`
	relXrdFilePath string
	ProjectFile    string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`

	projFS    afero.Fs
	exampleFS afero.Fs
}

// AfterApply constructs and binds Upbound-specific context to any subcommands
// that have Run() methods that receive it.
func (c *generateCmd) AfterApply(kongCtx *kong.Context, p pterm.TextPrinter) error {
	kongCtx.Bind(pterm.DefaultBulletList.WithWriter(kongCtx.Stdout))
	ctx := context.Background()

	// Read the project file.
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	// The location of the project file defines the root of the project.
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	// The location of the co position defines the root of the xrd.
	proj, err := project.Parse(c.projFS, c.ProjectFile)
	if err != nil {
		return err
	}

	c.exampleFS = afero.NewBasePathFs(
		c.projFS, proj.Spec.Paths.Examples,
	)

	c.relXrdFilePath = c.XRDFilePath
	if filepath.IsAbs(c.relXrdFilePath) {
		// Convert the absolute path to a relative path within projFS
		relPath, err := filepath.Rel(afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), "."), c.relXrdFilePath)
		if err != nil {
			return errors.Wrap(err, "failed to make file path relative to project filesystem")
		}

		// Check if relPath is within c.projFS
		if strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
			return errors.New("file path is outside the project filesystem")
		}

		c.relXrdFilePath = relPath
	}

	// workaround interfaces not being bindable ref: https://github.com/alecthomas/kong/issues/48
	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

func (c *generateCmd) Run(ctx context.Context) error {
	pterm.EnableStyling()
	// get xr or xrc/claim as input otherwise ask interactive
	if c.Type == "" {
		c.Type = c.getInteractiveType()
	}
	if len(c.relXrdFilePath) > 0 {
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

	xrdRaw, err := afero.ReadFile(c.projFS, c.relXrdFilePath)
	if err != nil {
		return xrd, errors.Wrapf(err, "failed to read file in %s", afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), c.relXrdFilePath))
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

	yamlData, err := icrd.GenerateExample(*crd, true, false)
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

	res.ObjectMeta.Name = strings.ToLower(res.Kind)
	if c.Type == "xrc" || c.Type == "claim" {
		res.ObjectMeta.Namespace = "default"
	}

	return res, nil
}

// processInput handles the logic when the XRD file path is not provided (interactive input)
func (c *generateCmd) processInput() error {
	resourceType, compositeName, apiGroup, apiVersion, name, namespace, err := c.collectInteractiveInput()
	if err != nil {
		return err
	}

	resource, err := c.createResource(resourceType, compositeName, apiGroup, apiVersion, name, namespace)
	if err != nil {
		return errors.Wrap(err, "failed to create xrd")
	}

	return c.outputResource(resource)
}

func (c *generateCmd) collectInteractiveInput() (string, string, string, string, string, string, error) {
	// Collect the resource type, kind, API group, API version, metadata.name and metadata.namespace
	return c.getInteractiveType(),
		c.getInteractiveKind(c.Type),
		c.getInteractiveGroup(),
		c.getInteractiveVersion(),
		c.getInteractiveMetadataName(),
		c.getInteractiveMetadataNamespace(c.Type),
		nil
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

// getInteractiveKind gets the resource kind interactively
func (c *generateCmd) getInteractiveKind(resourceType string) string {
	if c.Kind != "" {
		return c.Kind
	}

	var input pterm.InteractiveTextInputPrinter
	if resourceType == "xrc" {
		input = *pterm.DefaultInteractiveTextInput.
			WithDefaultText("What is your Composite Resource Claim (XRC) kind?").
			WithDefaultValue("Cluster")
	} else {
		input = *pterm.DefaultInteractiveTextInput.
			WithDefaultText("What is your Composite Resource (XR) kind?").
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

// getInteractiveMetadataName gets the metadata.name interactively
func (c *generateCmd) getInteractiveMetadataName() string {
	if c.Name != "" {
		return c.Name
	}

	input := *pterm.DefaultInteractiveTextInput.
		WithDefaultText("What is the metadata name?").
		WithDefaultValue("example")

	name, err := input.Show()
	if err != nil {
		pterm.Error.Println("An error occurred while getting metadata.name:", err)
		return ""
	}

	return name
}

// getInteractiveMetadataNamespace gets the metadata.namespace interactively
func (c *generateCmd) getInteractiveMetadataNamespace(resourceType string) string {
	if c.Namespace != "" {
		return c.Namespace
	}

	if resourceType != "xrc" {
		return ""
	}

	input := *pterm.DefaultInteractiveTextInput.
		WithDefaultText("What is the metadata namespace?").
		WithDefaultValue("default")

	namespace, err := input.Show()
	if err != nil {
		pterm.Error.Println("An error occurred while getting metadata.namespace:", err)
		return ""
	}

	return namespace
}

// createResource creates a resource based on the collected input
func (c *generateCmd) createResource(resourceType, compositeName, apiGroup, apiVersion, name, namespace string) (resource, error) {
	var res resource
	// Check if required fields are missing or invalid
	if compositeName == "" {
		return res, errors.New("compositeName is required")
	}
	if apiGroup == "" {
		return res, errors.New("apiGroup is required")
	}
	if resourceType == "" {
		return res, errors.New("resourceType is required")
	}
	if apiVersion == "" || !icrd.IsKnownAPIVersion(apiVersion) {
		return res, fmt.Errorf("apiVersion is required or invalid. Valid versions are: %v", icrd.KnownAPIVersions)
	}
	validatedNamespace, err := validateNameNamespace(name, namespace)
	if err != nil {
		return res, err
	}

	res = resource{
		TypeMeta: metav1.TypeMeta{
			APIVersion: fmt.Sprintf("%s/%s", apiGroup, apiVersion),
			Kind:       compositeName,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.ToLower(name),
		},
		Spec: map[string]interface{}{},
	}

	if resourceType == "xrc" || resourceType == "claim" {
		res.ObjectMeta.Namespace = strings.ToLower(validatedNamespace)
	}

	return res, nil
}

// outputResource handles the output of the generated resource based on the specified output type
func (c *generateCmd) outputResource(res resource) error { // nolint:gocyclo
	// Convert resource to YAML format
	resourceYAML, err := yaml.Marshal(res)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal resource to YAML")
	}

	switch c.Output {
	case outputFile:
		filePath := c.Path
		if filePath == "" {
			filePath = fmt.Sprintf("%s/%s.yaml", strings.ToLower(res.Kind), strings.ToLower(res.ObjectMeta.Name))
		}

		// Check if the example file already exists
		exists, err := afero.Exists(c.exampleFS, filePath)
		if err != nil {
			return errors.Wrap(err, "failed to check if file exists")
		}

		if exists {
			// Prompt the user for confirmation to merge
			pterm.Println() // Blank line for spacing
			confirm := pterm.DefaultInteractiveConfirm
			confirm.DefaultText = fmt.Sprintf("The example file '%s' already exists. Do you want to override its contents?", afero.FullBaseFsPath(c.exampleFS.(*afero.BasePathFs), filePath))
			confirm.DefaultValue = false

			result, _ := confirm.Show() // Display confirmation prompt
			pterm.Println()             // Blank line for spacing

			if !result {
				return errors.New("operation cancelled by user")
			}
		}

		if err := c.exampleFS.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return errors.Wrap(err, "failed to create directories for the specified output path")
		}

		if err := afero.WriteFile(c.exampleFS, filePath, resourceYAML, 0644); err != nil {
			return errors.Wrap(err, "failed to write example to file")
		}

		pterm.Printfln("Successfully created example and saved to %s", afero.FullBaseFsPath(c.exampleFS.(*afero.BasePathFs), filePath))

	case outputYAML:
		pterm.Println(string(resourceYAML))
	case outputJSON:
		jsonData, err := yaml.YAMLToJSON(resourceYAML)
		if err != nil {
			return errors.Wrapf(err, "failed to convert resource to JSON")
		}
		pterm.Println(string(jsonData))
	default:
		return errors.New("invalid output format specified")
	}

	return nil
}

// validateNameNamespace checks that the name and (if provided) the namespace are valid DNS labels
func validateNameNamespace(name, namespace string) (string, error) {
	if len(name) > 63 {
		return "", errors.New("metadata.name must be no more than 63 characters")
	}
	if !dnsLabelRegex.MatchString(name) {
		return "", errors.New("metadata.name is invalid: must be a valid DNS label (lowercase alphanumeric, may include hyphens)")
	}

	if namespace == "" {
		namespace = "default"
	} else {
		if len(namespace) > 63 {
			return "", errors.New("metadata.namespace must be no more than 63 characters")
		}
		if !dnsLabelRegex.MatchString(namespace) {
			return "", errors.New("metadata.namespace is invalid: must be a valid DNS label (lowercase alphanumeric, may include hyphens)")
		}
	}

	return namespace, nil
}
