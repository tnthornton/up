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

package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/gobuffalo/flect"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	xcrd "github.com/upbound/up/internal/crd"
	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/dep"
	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	mxpkg "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"
	projectv1alpha1 "github.com/upbound/up/pkg/apis/project/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

func (c *generateCmd) Help() string {
	return `
The 'generate' command creates a composition and adds the required function packages to the project as dependencies.

Examples:
    composition generate apis/xnetwork/definition.yaml
        Generates a composition from an CompositeResourceDefinition (XRD).
		Saves output to 'apis/xnetworks/composition.yaml'.

    composition generate examples/xnetwork/xnetwork.yaml
        Generates a composition from an Composite Resource (XR).
		Saves output to 'apis/xnetworks/composition.yaml'.

    composition generate examples/network/network-aws.yaml --name aws
        Generates a composition from the Composite Resource Claim (XRC) with labels
		if 'spec.compositionSelector.matchLabels' is set in the XR, using 'aws' as a prefix in 'metadata.name'.
		Saves output to 'apis/xnetworks/composition-aws.yaml'.

    composition generate examples/xnetwork/xnetwork-azure.yaml --name azure
        Generates a composition from the Composite Resource (XR) or Composite Resource Claim (XRC) with labels
		if 'spec.compositionSelector.matchLabels' is set in the XR, using 'azure' as a prefix in 'metadata.name'.
		Saves output to 'apis/xnetworks/composition-azure.yaml'.

    composition generate examples/xdatabase/database.yaml --plural postgreses
        Generates a composition from the Composite Resource (XR) with a custom plural form,
		Saves output to 'apis/xdatabases/composition.yaml'.
`
}

const (
	outputFile            = "file"
	outputYAML            = "yaml"
	outputJSON            = "json"
	errInvalidPkgName     = "invalid package dependency supplied"
	functionAutoReadyXpkg = "xpkg.upbound.io/crossplane-contrib/function-auto-ready"
)

var kclTemplate = `{
    "apiVersion": "template.fn.crossplane.io/v1beta1",
    "kind": "KCLInput",
    "spec": {
        "source": ""
    }
}`

var goTemplate = `{
    "apiVersion": "gotemplating.fn.crossplane.io/v1beta1",
    "kind": "GoTemplate",
    "source": "Inline",
    "inline": {
        "template": ""
    }
}`

var patTemplate = `{
    "apiVersion": "pt.fn.crossplane.io/v1beta1",
    "kind": "Resources",
    "resources": []
}`

type generateCmd struct {
	Resource string `arg:"" required:"" help:"File path to Composite Resource Claim (XRC) or Composite Resource (XR) or CompositeResourceDefinition (XRD)."`
	Name     string `optional:"" help:"Name for the new composition."`
	Plural   string `optional:"" help:"Optional custom plural for the CompositeTypeRef.Kind"`

	Path        string `optional:""  help:"Optional path to the output file where the generated Composition will be saved."`
	ProjectFile string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Output      string `help:"Output format for the results: 'file' to save to a file, 'yaml' to print XRD in YAML format, 'json' to print XRD in JSON format." short:"o" default:"file" enum:"file,yaml,json"`
	CacheDir    string `short:"d" help:"Directory used for caching dependency images." default:"~/.up/cache/" env:"CACHE_DIR" type:"path"`

	projFS afero.Fs
	apisFS afero.Fs
	proj   *projectv1alpha1.Project

	m  *manager.Manager
	ws *workspace.Workspace
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

	// The location of the co position defines the root of the composition.
	proj, err := project.Parse(c.projFS, c.ProjectFile)
	if err != nil {
		return err
	}

	c.proj = proj

	c.apisFS = afero.NewBasePathFs(
		c.projFS, proj.Spec.Paths.APIs,
	)

	fs := afero.NewOsFs()

	cache, err := cache.NewLocal(c.CacheDir, cache.WithFS(fs))
	if err != nil {
		return err
	}

	r := image.NewResolver()

	m, err := manager.New(
		manager.WithCache(cache),
		manager.WithResolver(r),
	)

	if err != nil {
		return err
	}

	c.m = m

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	ws, err := workspace.New(wd,
		workspace.WithFS(fs),
		// The user doesn't care about workspace warnings during composition generate.
		workspace.WithPrinter(&pterm.BasicTextPrinter{Writer: io.Discard}),
		workspace.WithPermissiveParser(),
	)
	if err != nil {
		return err
	}
	c.ws = ws

	if err := ws.Parse(ctx); err != nil {
		return err
	}

	// workaround interfaces not being bindable ref: https://github.com/alecthomas/kong/issues/48
	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

func (c *generateCmd) Run(ctx context.Context, p pterm.TextPrinter) error { // nolint:gocyclo
	pterm.EnableStyling()
	composition, plural, err := c.newComposition(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to create composition")
	}

	// get rid of metadata.creationTimestamp nil
	compositionClean := map[string]interface{}{
		"apiVersion": composition.APIVersion,
		"kind":       composition.Kind,
		"metadata": map[string]interface{}{
			"name": composition.ObjectMeta.Name,
		},
		"spec": composition.Spec,
	}

	// Add labels if they exist
	if len(composition.ObjectMeta.Labels) > 0 {
		if metadata, ok := compositionClean["metadata"].(map[string]interface{}); ok {
			metadata["labels"] = composition.ObjectMeta.Labels
		}
	}

	// Convert Composition to YAML format
	compositionYAML, err := yaml.Marshal(compositionClean)
	if err != nil {
		return errors.Wrap(err, "failed to marshal composition to yaml")
	}

	switch c.Output {
	case outputFile:
		// Determine the file path
		filePath := c.Path
		if filePath == "" {
			if c.Name != "" {
				filePath = fmt.Sprintf("%s/composition-%s.yaml", strings.ToLower(plural), c.Name)
			} else {
				filePath = fmt.Sprintf("%s/composition.yaml", strings.ToLower(plural))
			}
		}

		// Check if the composition already exists
		exists, err := afero.Exists(c.apisFS, filePath)
		if err != nil {
			return errors.Wrap(err, "failed to check if file exists")
		}

		// If the file exists, prompt the user for confirmation to overwrite
		if exists {

			pterm.Println() // Blank line for spacing
			confirm := pterm.DefaultInteractiveConfirm
			confirm.DefaultText = fmt.Sprintf("The Composition file '%s' already exists. Do you want to overwrite its contents?", afero.FullBaseFsPath(c.apisFS.(*afero.BasePathFs), filePath))
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

		// Write the YAML to the specified output file
		if err := afero.WriteFile(c.apisFS, filePath, compositionYAML, 0644); err != nil {
			return errors.Wrap(err, "failed to write composition to file")
		}

		p.Printfln("successfully created Composition and saved to %s", afero.FullBaseFsPath(c.apisFS.(*afero.BasePathFs), filePath))

	case outputYAML:
		p.Println(string(compositionYAML))

	case outputJSON:
		jsonData, err := yaml.YAMLToJSON(compositionYAML)
		if err != nil {
			return errors.Wrap(err, "failed to convert composition to JSON")
		}
		p.Println(string(jsonData))

	default:
		return errors.New("invalid output format specified")
	}

	return nil
}

// newComposition to create a new Composition
func (c *generateCmd) newComposition(ctx context.Context) (*v1.Composition, string, error) { // nolint:gocyclo
	group, version, kind, plural, matchLabels, err := c.processResource()
	if err != nil {
		return nil, "", errors.Wrap(err, "failed to load resource")
	}

	// Use custom name if provided, otherwise generate it
	name := c.Name
	if name == "" {
		name = strings.ToLower(fmt.Sprintf("%s.%s", plural, group))
	} else {
		name = strings.ToLower(fmt.Sprintf("%s.%s.%s", c.Name, plural, group))
	}

	pipelineSteps, err := c.createPipelineFromProject(ctx)
	if err != nil {
		return nil, "", errors.Wrap(err, "failed create pipelines from project")
	}

	composition := &v1.Composition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.CompositionGroupVersionKind.GroupVersion().String(),
			Kind:       v1.CompositionGroupVersionKind.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.CompositionSpec{
			CompositeTypeRef: v1.TypeReference{
				APIVersion: fmt.Sprintf("%s/%s", group, version),
				Kind:       kind,
			},
			Mode:     ptr.To(v1.CompositionModePipeline),
			Pipeline: pipelineSteps,
		},
	}

	if len(matchLabels) > 0 {
		composition.Labels = matchLabels
	}

	return composition, plural, nil
}

func (c *generateCmd) createPipelineFromProject(ctx context.Context) ([]v1.PipelineStep, error) { // nolint:gocyclo
	maxSteps := len(c.proj.Spec.DependsOn)
	pipelineSteps := make([]v1.PipelineStep, 0, maxSteps)
	foundAutoReadyFunction := false

	var deps []*mxpkg.ParsedPackage
	var err error

	for _, dep := range c.proj.Spec.DependsOn {
		if dep.Function != nil {
			functionName, err := name.ParseReference(*dep.Function)
			if err != nil {
				return nil, errors.Wrap(err, errInvalidPkgName)
			}

			// Check if auto-ready-function is available in deps
			if functionName.String() == functionAutoReadyXpkg {
				foundAutoReadyFunction = true
			}

			// Convert the dependency to v1beta1.Dependency
			convertedDep, ok := manager.ConvertToV1beta1(dep)
			if !ok {
				return nil, errors.Errorf("failed to convert dependency in %s", functionName)
			}

			// Try to resolve the function
			_, deps, err = c.m.Resolve(ctx, convertedDep)
			if err != nil {
				// If resolving fails, try to add function
				_, deps, err = c.m.AddAll(ctx, convertedDep)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to add dependencies in %s", functionName)
				}
			}
		}
	}

	if !foundAutoReadyFunction {
		d := dep.New(functionAutoReadyXpkg)

		var ud v1beta1.Dependency
		ud, deps, err = c.m.AddAll(ctx, d)
		if err != nil {
			return nil, errors.Wrap(err, "failed to add auto-ready dependency")
		}

		meta := c.ws.View().Meta()
		if meta != nil {
			if d.Constraints != "" {
				ud.Constraints = d.Constraints
			}
			if err := meta.Upsert(ud); err != nil {
				return nil, errors.Wrap(err, "failed to add auto-ready dependency")
			}

			if err := c.ws.Write(meta); err != nil {
				return nil, errors.Wrap(err, "failed to write auto-ready dependency to project")
			}
		}

	}

	for _, dep := range deps {
		var rawExtension *runtime.RawExtension
		if len(dep.Objs) > 0 {
			kind := dep.Objs[0].GetObjectKind().GroupVersionKind()
			if kind.Kind == "CustomResourceDefinition" && kind.GroupVersion().String() == "apiextensions.k8s.io/v1" {
				if crd, ok := dep.Objs[0].(*apiextensionsv1.CustomResourceDefinition); ok {
					rawExtension, err = c.setRawExtension(*crd)
					if err != nil {
						return nil, errors.Wrap(err, "failed to generate rawExtension for input")
					}
				} else {
					return nil, errors.New("failed to use to CustomResourceDefinition")
				}
			}
		}

		functionName, err := name.ParseReference(dep.DepName)
		if err != nil {
			return nil, errors.Wrap(err, errInvalidPkgName)
		}

		// create a PipelineStep for each function
		step := v1.PipelineStep{
			Step: xpkg.ToDNSLabel(functionName.Context().RepositoryStr()),
			FunctionRef: v1.FunctionReference{
				Name: xpkg.ToDNSLabel(functionName.Context().RepositoryStr()),
			},
		}
		if rawExtension != nil {
			step.Input = rawExtension
		}

		pipelineSteps = append(pipelineSteps, step)
	}

	if len(pipelineSteps) == 0 {
		return nil, errors.New("no function found")
	}

	return reorderPipelineSteps(pipelineSteps), nil
}

// reorderPipelineSteps ensures the step with functionref.name == "crossplane-contrib-function-auto-ready" is the last one
func reorderPipelineSteps(pipelineSteps []v1.PipelineStep) []v1.PipelineStep {
	var reorderedSteps []v1.PipelineStep
	var autoReadyStep *v1.PipelineStep

	// Iterate through the steps and separate the "crossplane-contrib-function-auto-ready" step
	for _, step := range pipelineSteps {
		// Create a copy of step to avoid memory aliasing issues
		currentStep := step
		if step.FunctionRef.Name == "crossplane-contrib-function-auto-ready" {
			autoReadyStep = &currentStep
		} else {
			reorderedSteps = append(reorderedSteps, currentStep)
		}
	}

	// If we found the auto-ready step, append it at the end
	if autoReadyStep != nil {
		reorderedSteps = append(reorderedSteps, *autoReadyStep)
	}

	return reorderedSteps
}

func (c *generateCmd) setRawExtension(crd apiextensionsv1.CustomResourceDefinition) (*runtime.RawExtension, error) { // nolint:gocyclo
	var rawExtensionContent string
	// Get the version using the modified getVersion function
	version, err := xcrd.GetCRDVersion(crd)
	if err != nil {
		return nil, err
	}

	gvk := fmt.Sprintf("%s/%s/%s", crd.Spec.Group, version, crd.Spec.Names.Kind)

	// match GVK and inputType to create the appropriate raw extension content
	switch gvk {
	case "template.fn.crossplane.io/v1beta1/KCLInput":
		rawExtensionContent = kclTemplate

	case "gotemplating.fn.crossplane.io/v1beta1/GoTemplate":
		rawExtensionContent = goTemplate

	case "pt.fn.crossplane.io/v1beta1/Resources":
		rawExtensionContent = patTemplate
	default:
		// nothing matches so we generate the default required fields
		// only required fields from function crd
		yamlData, err := xcrd.GenerateExample(crd, true, false)
		if err != nil {
			return nil, errors.Wrap(err, "failed generating schema")
		}
		jsonData, err := json.Marshal(yamlData)
		if err != nil {
			return nil, errors.Wrap(err, "failed marshaling to JSON")
		}
		rawExtensionContent = string(jsonData)
	}

	raw := &runtime.RawExtension{
		Raw: []byte(rawExtensionContent),
	}

	return raw, nil
}

func (c *generateCmd) processResource() (string, string, string, string, map[string]string, error) {
	resourceRaw, err := afero.ReadFile(c.projFS, c.Resource)
	if err != nil {
		return "", "", "", "", nil, errors.Wrap(err, "failed to read resource file")
	}

	// Create an unstructured object
	var unstructuredObj unstructured.Unstructured
	if err := yaml.Unmarshal(resourceRaw, &unstructuredObj.Object); err != nil {
		return "", "", "", "", nil, errors.Wrap(err, "failed to unmarshal resource into unstructured object")
	}

	// Check if obj is a CompositeResourceDefinition by examining its "kind"
	if unstructuredObj.GetKind() == "CompositeResourceDefinition" {

		// Convert unstructured to CompositeResourceDefinition
		var xrd v1.CompositeResourceDefinition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.Object, &xrd); err != nil {
			return "", "", "", "", nil, errors.Wrap(err, "failed to convert unstructured object to CompositeResourceDefinition")
		}

		// Successfully identified as CompositeResourceDefinition, extract fields
		group := xrd.Spec.Group
		version, err := xcrd.GetXRDVersion(xrd)
		if err != nil {
			return "", "", "", "", nil, errors.Wrap(err, "failed to retrieve XRD version")
		}
		kind := xrd.Spec.Names.Kind
		plural := xrd.Spec.Names.Plural

		return group, version, kind, plural, nil, nil
	}

	// Fallback: If not a CompositeResourceDefinition, process generically
	gvk := unstructuredObj.GroupVersionKind()
	plural := c.Plural
	if plural == "" {
		plural = flect.Pluralize(gvk.Kind)
	}

	// Attempt to extract matchLabels from spec.compositionSelector.matchLabels
	matchLabels := map[string]string{}
	labels, found, err := unstructured.NestedStringMap(unstructuredObj.Object, "spec", "compositionSelector", "matchLabels")
	if err != nil {
		return "", "", "", "", nil, errors.Wrap(err, "failed to extract matchLabels from resource spec")
	}
	if found {
		matchLabels = labels
	}

	// Return the gathered information along with any matchLabels
	return gvk.Group, gvk.Version, gvk.Kind, plural, matchLabels, nil
}
