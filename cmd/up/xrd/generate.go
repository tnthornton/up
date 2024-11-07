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
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/gobuffalo/flect"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"

	"github.com/upbound/up/internal/async"
	"github.com/upbound/up/internal/project"
	projectv1alpha1 "github.com/upbound/up/pkg/apis/project/v1alpha1"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/schemagenerator"
	"github.com/upbound/up/internal/xpkg/schemarunner"
	"github.com/upbound/up/internal/yaml"
)

func (c *generateCmd) Help() string {
	return `
The 'generate' command creates a CompositeResourceDefinition (XRD) from a given Composite Resource (XR) or Composite Resource Claim (XRC) and generates associated language models for function usage.

Usage Examples:
    xrd generate examples/cluster/example.yaml
        Generates a CompositeResourceDefinition (XRD) based on the specified Composite Resource or Claim and saves it to the default APIs folder in the project.

    xrd generate examples/postgres/example.yaml --plural postgreses
        Generates a CompositeResourceDefinition (XRD) with a specified plural form, useful for cases where automatic pluralization may not be accurate (e.g., "postgres").

    xrd generate examples/postgres/example.yaml --path database/definition.yaml
        Generates a CompositeResourceDefinition (XRD) and saves it to a custom path within the project's default APIs folder.
`
}

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
	File     string `arg:"" help:"Path to the file containing the Composite Resource (XR) or Composite Resource Claim (XRC)."`
	CacheDir string `short:"d" help:"Directory used for caching dependency images." default:"~/.up/cache/" env:"CACHE_DIR" type:"path"`
	Path     string `help:"Path to the output file where the Composite Resource Definition (XRD) will be saved." optional:""`
	Plural   string `help:"Optional custom plural form for the Composite Resource Definition (XRD)." optional:""`
	Output   string `help:"Output format for the results: 'file' to save to a file, 'yaml' to print XRD in YAML format, 'json' to print XRD in JSON format." short:"o" default:"file" enum:"file,yaml,json"`

	ProjectFile string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`

	projFS   afero.Fs
	apisFS   afero.Fs
	modelsFS afero.Fs
	proj     *projectv1alpha1.Project
	relFile  string

	schemarunner schemarunner.SchemaRunner
	m            *manager.Manager
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
	c.modelsFS = afero.NewBasePathFs(afero.NewOsFs(), filepath.Join(projDirPath, ".up"))
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	// The location of the co position defines the root of the xrd.
	proj, err := project.Parse(c.projFS, c.ProjectFile)
	if err != nil {
		return err
	}

	c.proj = proj

	c.apisFS = afero.NewBasePathFs(
		c.projFS, proj.Spec.Paths.APIs,
	)

	c.relFile = c.File
	if filepath.IsAbs(c.File) {
		// Convert the absolute path to a relative path within projFS
		relPath, err := filepath.Rel(afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), "."), c.File)
		if err != nil {
			return errors.Wrap(err, "failed to make file path relative to project filesystem")
		}

		// Check if relPath is within c.projFS
		if strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
			return errors.New("file path is outside the project filesystem")
		}

		c.relFile = relPath
	}

	fs := afero.NewOsFs()

	cache, err := cache.NewLocal(c.CacheDir, cache.WithFS(fs))
	if err != nil {
		return err
	}

	r := image.NewResolver()

	m, err := manager.New(
		manager.WithCacheModels(c.modelsFS),
		manager.WithCache(cache),
		manager.WithResolver(r),
	)

	if err != nil {
		return err
	}

	c.m = m

	c.schemarunner = schemarunner.RealSchemaRunner{}

	// workaround interfaces not being bindable ref: https://github.com/alecthomas/kong/issues/48
	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

func (c *generateCmd) Run(ctx context.Context, p pterm.TextPrinter) error { // nolint:gocyclo
	pterm.EnableStyling()
	yamlData, err := afero.ReadFile(c.projFS, c.relFile)
	if err != nil {
		return errors.Wrapf(err, "failed to read file in %s", afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), c.relFile))
	}

	xrd, err := newXRD(yamlData, c.Plural)
	if err != nil {
		return errors.Wrap(err, "failed to create CompositeResourceDefinition (XRD)")
	}

	// Convert XRD to YAML format
	xrdYAML, err := yaml.Marshal(xrd, yaml.RemoveField("status"))
	if err != nil {
		return errors.Wrap(err, "failed to marshal XRD to YAML")
	}

	switch c.Output {
	case outputFile:
		// Determine the file path
		filePath := c.Path
		if filePath == "" {
			filePath = fmt.Sprintf("%s/definition.yaml", xrd.Spec.Names.Plural)
		}

		// Check if the composition file already exists
		exists, err := afero.Exists(c.apisFS, filePath)
		if err != nil {
			return errors.Wrap(err, "failed to check if file exists")
		}

		if exists {
			// Prompt the user for confirmation to merge
			pterm.Println() // Blank line for spacing
			confirm := pterm.DefaultInteractiveConfirm
			confirm.DefaultText = fmt.Sprintf("The CompositeResourceDefinition (XRD) file '%s' already exists. Do you want to override its contents?", afero.FullBaseFsPath(c.apisFS.(*afero.BasePathFs), filePath))
			confirm.DefaultValue = false

			result, _ := confirm.Show() // Display confirmation prompt
			pterm.Println()             // Blank line for spacing

			if !result {
				return errors.New("operation cancelled by user")
			}
		}

		if err := c.apisFS.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return errors.Wrap(err, "failed to create directories for the specified output path")
		}

		if err := afero.WriteFile(c.apisFS, filePath, xrdYAML, 0644); err != nil {
			return errors.Wrap(err, "failed to write CompositeResourceDefinition (XRD) to file")
		}

		// In parallel:
		// * Generate schemas for XRDs
		if err = async.WrapWithSuccessSpinners(func(ch async.EventChannel) error {
			eg, ctx := errgroup.WithContext(ctx)

			eg.Go(func() error {
				var err error
				kfs, err := schemagenerator.GenerateSchemaKcl(ctx, c.apisFS, []string{}, c.schemarunner)
				if err != nil {
					return err
				}

				if err := c.m.AddModels("kcl", kfs); err != nil {
					return err
				}
				return err
			})

			eg.Go(func() error {
				var err error
				pfs, err := schemagenerator.GenerateSchemaPython(ctx, c.apisFS, []string{}, c.schemarunner)
				if err != nil {
					return err
				}

				if err := c.m.AddModels("python", pfs); err != nil {
					return err
				}
				return err
			})

			return eg.Wait()
		}); err != nil {
			return err
		}

		p.Printfln("Successfully created CompositeResourceDefinition (XRD) and saved to %s", afero.FullBaseFsPath(c.apisFS.(*afero.BasePathFs), filePath))

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
		return nil, errors.Wrap(err, "failed to unmarshal YAML")
	}

	// Ensure only allowed top-level keys: apiVersion, kind, metadata, spec, and status
	var topLevelKeys map[string]interface{}
	err = yaml.Unmarshal(yamlData, &topLevelKeys)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal YAML to check top-level keys")
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
		return nil, errors.Wrap(err, "failed to parse API version")
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
		return nil, errors.Wrap(err, "failed to infer properties for spec")
	}

	statusProps, err := inferProperties(input.Status)
	if err != nil {
		return nil, errors.Wrap(err, "failed to infer properties for status")
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
		return nil, errors.Wrap(err, "failed to marshal OpenAPI v3 schema")
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
