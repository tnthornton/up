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
	"os"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/pterm/pterm"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xcrd "github.com/upbound/up/internal/crd"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	mxpkg "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
	projectv1alpha1 "github.com/upbound/up/pkg/apis/project/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

const (
	outputFile            = "file"
	outputYAML            = "yaml"
	outputJSON            = "json"
	errInvalidPkgName     = "invalid package dependency supplied"
	functionAutoReadyXpkg = "xpkg.upbound.io/crossplane-contrib/function-auto-ready"
)

type generateCmd struct {
	XRD         string `arg:"" help:"File path to the Crossplane Resource Definition (XRD) YAML file."`
	Path        string `help:"Optional path to the output file where the generated Composition will be saved." optional:""`
	ProjectFile string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Output      string `help:"Output format for the results: 'file' to save to a file, 'yaml' to print XRD in YAML format, 'json' to print XRD in JSON format." short:"o" default:"file" enum:"file,yaml,json"`
}

func (c *generateCmd) Run(ctx context.Context, p pterm.TextPrinter) error { // nolint:gocyclo
	xrdRaw, err := os.ReadFile(c.XRD)
	if err != nil {
		return errors.Wrapf(err, "failed to read xrd file")
	}

	projectRaw, err := os.ReadFile(c.ProjectFile)
	if err != nil {
		return errors.Wrapf(err, "failed to read upbound project file")
	}

	var xrd v1.CompositeResourceDefinition
	err = yaml.Unmarshal(xrdRaw, &xrd)
	if err != nil {
		return errors.Wrapf(err, "failed to unmarshal to xrd")
	}

	var project projectv1alpha1.Project
	err = yaml.Unmarshal(projectRaw, &project)
	if err != nil {
		return errors.Wrapf(err, "failed to unmarshal to project")
	}

	composition, err := c.newComposition(ctx, xrd, project)
	if err != nil {
		return errors.Wrapf(err, "failed to create composition")
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

	// Convert Composition to YAML format
	compositionYAML, err := yaml.Marshal(compositionClean)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal composition to yaml")
	}

	switch c.Output {
	case outputFile:
		// Determine the file path
		filePath := c.Path
		if filePath == "" {
			filePath = fmt.Sprintf("apis/%s/composition.yaml", xrd.Spec.Names.Plural)
		}

		// Ensure the directory exists before writing the file
		outputDir := filepath.Dir(filepath.Clean(filePath))
		if err = os.MkdirAll(outputDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "failed to create output directory")
		}

		// Write the YAML to the specified output file
		if err = os.WriteFile(filePath, compositionYAML, 0644); err != nil { // nolint:gosec // writing to file
			return errors.Wrapf(err, "failed to write composition to file")
		}

		p.Printfln("successfully created Composition and saved to %s", filePath)

	case outputYAML:
		p.Println(string(compositionYAML))

	case outputJSON:
		jsonData, err := yaml.YAMLToJSON(compositionYAML)
		if err != nil {
			return errors.Wrapf(err, "failed to convert composition to JSON")
		}
		p.Println(string(jsonData))

	default:
		return errors.New("invalid output format specified")
	}

	return nil
}

// newComposition to create a new Composition
func (c *generateCmd) newComposition(ctx context.Context, xrd v1.CompositeResourceDefinition, project projectv1alpha1.Project) (*v1.Composition, error) { // nolint:gocyclo
	group := xrd.Spec.Group
	version, err := xcrd.GetXRDVersion(xrd)
	if err != nil {
		return nil, errors.Wrapf(err, "version not found")
	}
	kind := xrd.Spec.Names.Kind
	name := xrd.Name

	pipelineSteps, err := c.createPipelineFromProject(ctx, project)
	if err != nil {
		return nil, errors.Wrapf(err, "failed create pipelines from project")
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
	return composition, nil
}

func (c *generateCmd) createPipelineFromProject(ctx context.Context, project projectv1alpha1.Project) ([]v1.PipelineStep, error) { // nolint:gocyclo
	maxSteps := len(project.Spec.DependsOn)
	pipelineSteps := make([]v1.PipelineStep, 0, maxSteps)
	foundAutoReadyFunction := false

	var deps []*mxpkg.ParsedPackage
	m, err := manager.New()
	if err != nil {
		return nil, errors.Wrap(err, "failed initializing manager")
	}

	for _, dep := range project.Spec.DependsOn {
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
			_, deps, err = m.Resolve(ctx, convertedDep)
			if err != nil {
				// If resolving fails, try to add function
				_, deps, err = m.AddAll(ctx, convertedDep)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to add dependencies in %s", functionName)
				}
			}
		}
	}

	if !foundAutoReadyFunction {
		autoReadyDep := v1beta1.Dependency{}
		autoReadyDep.Package = functionAutoReadyXpkg
		autoReadyDep.Type = "Function"

		_, deps, err = m.AddAll(ctx, autoReadyDep)

		if err != nil {
			return nil, errors.Wrapf(err, "failed to add auto-ready dependency")
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
						return nil, errors.Wrapf(err, "failed to generate rawExtension for input")
					}
				} else {
					return nil, errors.Errorf("failed to use to CustomResourceDefinition")
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
		rawExtensionContent = `{
	            "apiVersion": "template.fn.crossplane.io/v1beta1",
	            "kind": "KCLInput",
	            "spec": {
	                "source": ""
	            }
	        }`

	case "gotemplating.fn.crossplane.io/v1beta1/GoTemplate":
		rawExtensionContent = `{
	            "apiVersion": "gotemplating.fn.crossplane.io/v1beta1",
	            "kind": "GoTemplate",
	            "source": "Inline",
				"inline": {
				    "template": ""
				}
	        }`

	case "pt.fn.crossplane.io/v1beta1/Resources":
		rawExtensionContent = `{
	            "apiVersion": "pt.fn.crossplane.io/v1beta1",
	            "kind": "Resources",
	            "resources": []
	        }`
	default:
		// nothing matches so we generate the default required fields
		// only required fields from function crd
		yamlData, err := xcrd.GenerateExample(crd, true, false)
		if err != nil {
			return nil, errors.Wrapf(err, "failed generating schema")
		}
		jsonData, err := json.Marshal(yamlData)
		if err != nil {
			return nil, errors.Wrapf(err, "failed marshaling to JSON")
		}
		rawExtensionContent = string(jsonData)
	}

	raw := &runtime.RawExtension{
		Raw: []byte(rawExtensionContent),
	}

	return raw, nil
}
