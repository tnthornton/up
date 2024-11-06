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

package function

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/upbound/up/internal/filesystem"
	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/upterm"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"
	"github.com/upbound/up/internal/yaml"
)

func (c *generateCmd) Help() string {
	return `
The 'generate' command creates an embedded function in the specified language.

Examples:
    function generate fn1
        Creates a function with the default language (KCL) in the folder 'functions/fn1'.

    function generate fn2 --language python
        Creates a function with Python language support in the folder 'functions/fn2'.

    function generate xcluster /apis/xcluster/composition.yaml
        Creates a function with the default language (KCL) in the folder 'functions/xcluster'
        and adds a composition pipeline step with the function reference name specified in the given composition file.
`
}

const kclModTemplate = `[package]
name = "{{.Name}}"
version = "0.0.1"

[dependencies]
models = { path = "./model" }
`

const kclModLockTemplate = `[dependencies]
  [dependencies.model]
    name = "model"
    full_name = "models_0.0.1"
    version = "0.0.1"
`

const kclMainTemplate = `{{- if .Imports }}
{{- range .Imports }}
import {{.ImportPath}} as {{.Alias}}
{{- end }}
{{- "\n" }}
{{- end }}
oxr = option("params").oxr # observed composite resource
_ocds = option("params").ocds # observed composed resources
_dxr = option("params").dxr # desired composite resource
dcds = option("params").dcds # desired composed resources

_metadata = lambda name: str -> any {
    { annotations = { "krm.kcl.dev/composition-resource-name" = name }}
}

# Example to retrieve variables from "xr"; update as needed
# _region = "us-east-1"
# if oxr.spec?.parameters?.region:
#     _region = oxr.spec.parameters.region

_items = [
# Example S3 Bucket managed resource configuration; update as needed
# s3v1beta2.Bucket{
#     metadata: _metadata("my-bucket")
#     spec: {
#         forProvider: {
#             region: _region
#         }
#     }
# }
]
items = _items
`

const pythonReqTemplate = `crossplane-function-sdk-python==0.5.0
pydantic==2.9.2
`

const pythonMainTemplate = `from crossplane.function import resource
from crossplane.function.proto.v1 import run_function_pb2 as fnv1

def compose(req: fnv1.RunFunctionRequest, rsp: fnv1.RunFunctionResponse):
    pass
    # Example to retrieve variables from "xr"; update as needed
    # observed_xr = v1alpha1.XBucket(**req.observed.composite.resource)
    # region = "us-west-1"
    # if observed_xr.spec.region is not None:
    #     region = observed_xr.spec.region

    # Example S3 Bucket managed resource configuration; update as needed
    # bucket = v1beta1.Bucket(
    #     apiVersion="s3.aws.upbound.io/v1beta1",
    #     kind="Bucket",
    #     spec=v1beta1.Spec(
    #         forProvider=v1beta1.ForProvider(
    #             region=region,
    #         ),
    #     ),
    # )

    # resource.update(rsp.desired.resources["bucket"], bucket)
`

type kclModInfo struct {
	Name string
}

// Prepare formatted import paths for the template
type kclImportStatement struct {
	ImportPath string
	Alias      string
}

type generateCmd struct {
	ProjectFile     string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Repository      string `optional:"" help:"Repository for the built package. Overrides the repository specified in the project file."`
	CacheDir        string `short:"d" help:"Directory used for caching dependency images." default:"~/.up/cache/" env:"CACHE_DIR" type:"path"`
	Language        string `help:"Language for function." default:"kcl" enum:"kcl,python" short:"l"`
	Name            string `arg:"" required:"" help:"Name for the new Function."`
	CompositionPath string `arg:"" optional:"" help:"Path to Crossplane Composition file."`

	functionFS        afero.Fs
	modelsFS          afero.Fs
	projFS            afero.Fs
	projectRepository string
	fsPath            string

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
	c.modelsFS = afero.NewBasePathFs(afero.NewOsFs(), filepath.Join(projDirPath, ".up"))

	// The location of the co position defines the root of the function.
	proj, err := project.Parse(c.projFS, c.ProjectFile)
	if err != nil {
		return err
	}

	c.fsPath = filepath.Join(
		proj.Spec.Paths.Functions,
		c.Name,
	)

	c.projectRepository = proj.Spec.Repository
	c.functionFS = afero.NewBasePathFs(
		c.projFS, c.fsPath,
	)

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

	ws, err := workspace.New("/",
		workspace.WithFS(c.projFS),
		// The user doesn't care about workspace warnings during function generate.
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

	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

func (c *generateCmd) Run(ctx context.Context, p pterm.TextPrinter) error { // nolint:gocyclo
	var (
		err                error
		functionSpecificFs = afero.NewBasePathFs(afero.NewOsFs(), ".")
	)
	pterm.EnableStyling()

	if errs := validation.IsDNS1035Label(c.Name); len(errs) > 0 {
		return errors.Errorf("'%s' is not a valid function name. DNS-1035 constraints: %s", c.Name, strings.Join(errs, "; "))
	}

	if c.CompositionPath != "" {
		exists, _ := afero.Exists(c.projFS, c.CompositionPath)
		if !exists {
			return errors.Errorf("composition file %q does not exist", c.CompositionPath)
		}
	}

	isEmpty, err := filesystem.IsFsEmpty(c.functionFS)
	if err != nil {
		pterm.Error.Println("Failed to check if the filesystem is empty:", err)
		return err
	}

	if !isEmpty {
		// Prompt the user for confirmation to overwrite
		pterm.Println() // Blank line
		confirm := pterm.DefaultInteractiveConfirm
		confirm.DefaultText = fmt.Sprintf("The folder '%s' is not empty. Do you want to overwrite its contents?", afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), c.fsPath))
		confirm.DefaultValue = false
		result, _ := confirm.Show()
		pterm.Println() // Blank line

		if !result {
			pterm.Error.Println("The operation was cancelled. The function folder must be empty to proceed with the generation.")
			return errors.New("operation cancelled by user")
		}
	}

	err = upterm.WrapWithSuccessSpinner("Checking dependencies", upterm.CheckmarkSuccessSpinner, func() error {
		deps, _ := c.ws.View().Meta().DependsOn()

		// Check all dependencies in the cache
		for _, dep := range deps {
			_, _, err := c.m.AddAll(ctx, dep)
			if err != nil {
				return errors.Wrapf(err, "failed to check dependencies for %v", dep)
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	switch c.Language {
	case "kcl":
		functionSpecificFs, err = c.generateKCLFiles()
		if err != nil {
			return errors.Wrap(err, "failed to handle kcl")
		}
	case "python":
		functionSpecificFs, err = generatePythonFiles()
		if err != nil {
			return errors.Wrap(err, "failed to handle python")
		}
	default:
		return errors.Errorf("unsupported language: %s", c.Language)
	}

	err = upterm.WrapWithSuccessSpinner(
		"Generating Function Folder",
		upterm.CheckmarkSuccessSpinner,
		func() error {
			if err := filesystem.CopyFilesBetweenFs(functionSpecificFs, c.functionFS); err != nil {
				return errors.Wrap(err, "failed to copy files to function target")
			}

			modelsPath := ".up/" + c.Language + "/models"

			if err := filesystem.CreateSymlink(c.functionFS.(*afero.BasePathFs), "model", c.projFS.(*afero.BasePathFs), modelsPath); err != nil {
				return errors.Wrapf(err, "error creating models symlink")
			}

			return nil
		})
	if err != nil {
		return err
	}

	if c.CompositionPath != "" {
		err = upterm.WrapWithSuccessSpinner(
			"Adding Pipeline Step in Composition",
			upterm.CheckmarkSuccessSpinner,
			func() error {
				comp, err := c.readAndUnmarshalComposition()
				if err != nil {
					return errors.Wrapf(err, "failed to read composition")
				}

				if err := c.addPipelineStep(comp); err != nil {
					return errors.Wrap(err, "failed to add pipeline step to composition")
				}
				return nil
			})
		if err != nil {
			return err
		}
	}

	pterm.Printfln("successfully created Function and saved to %s", afero.FullBaseFsPath(c.projFS.(*afero.BasePathFs), c.fsPath))
	return nil
}

func (c *generateCmd) generateKCLFiles() (afero.Fs, error) { // nolint:gocyclo
	targetFS := afero.NewMemMapFs()

	kclModInfo := kclModInfo{
		Name: c.Name,
	}

	kclModPath := "kcl.mod"
	file, err := targetFS.Create(filepath.Clean(kclModPath))
	if err != nil {
		return nil, errors.Wrapf(err, "error creating file: %v", kclModPath)
	}

	tmpl := template.Must(template.New("toml").Parse(kclModTemplate))
	if err := tmpl.Execute(file, kclModInfo); err != nil {
		return nil, errors.Wrapf(err, "Error writing template to file: %v", kclModPath)
	}

	kclModLockPath := "kcl.mod.lock"
	if exists, err := afero.Exists(targetFS, kclModLockPath); err != nil {
		return nil, errors.Wrapf(err, "error checking file existence: %v", kclModLockPath)
	} else if !exists {
		file, err := targetFS.Create(filepath.Clean(kclModLockPath))
		if err != nil {
			return nil, errors.Wrapf(err, "error creating file: %v", kclModLockPath)
		}

		_, err = file.WriteString(kclModLockTemplate)
		if err != nil {
			return nil, errors.Wrapf(err, "error writing to file: %v", kclModLockPath)
		}
	}
	mainPath := "main.k"
	file, err = targetFS.Create(filepath.Clean(mainPath))
	if err != nil {
		return nil, errors.Wrapf(err, "error creating file: %v", mainPath)
	}
	foundFolders, _ := filesystem.FindNestedFoldersWithPattern(c.modelsFS, "kcl/models", "*.k")

	importStatements := make([]kclImportStatement, 0, len(foundFolders))
	for _, folder := range foundFolders {
		importPath, alias := formatKclImportPath(folder)
		importStatements = append(importStatements, kclImportStatement{
			ImportPath: importPath,
			Alias:      alias,
		})
	}
	mainTemplateData := struct {
		Imports []kclImportStatement
	}{
		Imports: importStatements,
	}
	mainTmpl := template.Must(template.New("kcl").Parse(kclMainTemplate))
	if err := mainTmpl.Execute(file, mainTemplateData); err != nil {
		return nil, errors.Wrapf(err, "Error writing KCL template to file: %v", mainPath)
	}

	return targetFS, nil
}

func generatePythonFiles() (afero.Fs, error) {
	targetFS := afero.NewMemMapFs()

	mainPath := "main.py"
	pythonReqPath := "requirements.txt"

	if exists, err := afero.Exists(targetFS, pythonReqPath); err != nil {
		return nil, errors.Wrapf(err, "error checking file existence: %v", pythonReqPath)
	} else if !exists {
		file, err := targetFS.Create(filepath.Clean(pythonReqPath))
		if err != nil {
			return nil, errors.Wrapf(err, "error creating file: %v", pythonReqPath)
		}

		_, err = file.WriteString(pythonReqTemplate)
		if err != nil {
			return nil, errors.Wrapf(err, "error writing to file: %v", pythonReqPath)
		}
	}

	if exists, err := afero.Exists(targetFS, mainPath); err != nil {
		return nil, errors.Wrapf(err, "error checking file existence: %v", mainPath)
	} else if !exists {
		file, err := targetFS.Create(filepath.Clean(mainPath))
		if err != nil {
			return nil, errors.Wrapf(err, "error creating file: %v", mainPath)
		}

		_, err = file.WriteString(pythonMainTemplate)
		if err != nil {
			return nil, errors.Wrapf(err, "error writing to file: %v", mainPath)
		}
	}

	return targetFS, nil
}

func (c *generateCmd) addPipelineStep(comp *v1.Composition) error {
	fnRepo := fmt.Sprintf("%s_%s", c.projectRepository, c.Name)
	ref, err := name.ParseReference(fnRepo)
	if err != nil {
		return errors.Wrapf(err, "error unable to parse the function repo")
	}

	step := v1.PipelineStep{
		Step: c.Name,
		FunctionRef: v1.FunctionReference{
			Name: xpkg.ToDNSLabel(ref.Context().RepositoryStr()),
		},
	}

	// Check if the step already exists in the pipeline
	for _, existingStep := range comp.Spec.Pipeline {
		if existingStep.Step == step.Step && existingStep.FunctionRef.Name == step.FunctionRef.Name {
			// Step already exists, no need to add it
			return nil
		}
	}

	comp.Spec.Pipeline = append([]v1.PipelineStep{step}, comp.Spec.Pipeline...)
	compYAML, err := yaml.Marshal(comp)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal composition to yaml")
	}

	if err = afero.WriteFile(c.projFS, c.CompositionPath, compYAML, 0644); err != nil {
		return errors.Wrapf(err, "failed to write composition to file")
	}

	return nil
}

func (c *generateCmd) readAndUnmarshalComposition() (*v1.Composition, error) {
	file, err := c.projFS.Open(c.CompositionPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open composition file")
	}

	compRaw, err := io.ReadAll(file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read composition file")
	}

	var comp v1.Composition
	err = yaml.Unmarshal(compRaw, &comp)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal to composition")
	}

	return &comp, nil
}

// Helper function to convert kcl paths to the desired import format
func formatKclImportPath(path string) (string, string) {
	// Find the position of "models" in the path and keep only the part after it
	modelsIndex := strings.Index(path, "models")
	if modelsIndex == -1 {
		return "", ""
	}

	// Trim everything before "models" and replace slashes with dots
	importPath := strings.ReplaceAll(path[modelsIndex:], "/", ".")

	// Extract alias using the last two components of the path
	parts := strings.Split(importPath, ".")
	alias := parts[len(parts)-2] + parts[len(parts)-1] // e.g., redshiftv1beta1

	return importPath, alias
}
