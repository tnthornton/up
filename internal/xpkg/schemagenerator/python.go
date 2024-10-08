// Copyright 2024 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package schemagenerator

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/exp/slices"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	xpv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	xcrd "github.com/upbound/up/internal/crd"
	"github.com/upbound/up/internal/filesystem"
	"github.com/upbound/up/internal/xpkg/schemarunner"
)

const (
	pythonModelsFolder         = "models"
	pythonAdoptModelsStructure = "sorted"
	pythonGeneratedFolder      = "models/workdir"
)

// GenerateSchemaPython generates Python schema files from the XRDs and CRDs fromFS
func GenerateSchemaPython(ctx context.Context, fromFS afero.Fs, exclude []string, generator schemarunner.SchemaRunner) (afero.Fs, error) { //nolint:gocyclo

	crdFS := afero.NewMemMapFs()
	schemaFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := crdFS.MkdirAll(baseFolder, 0755); err != nil {
		return nil, err
	}

	var openAPIPaths []string

	// Walk the virtual filesystem to find and process target files
	if err := afero.Walk(fromFS, "/", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip excluded paths
		for _, excl := range exclude {
			if strings.HasPrefix(path, excl) {
				return filepath.SkipDir
			}
		}

		if info.IsDir() {
			return nil
		}
		// Ignore files without yaml extensions.
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		var u metav1.TypeMeta
		bs, err := afero.ReadFile(fromFS, path)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %q", path)
		}
		err = yaml.Unmarshal(bs, &u)
		if err != nil {
			return errors.Wrapf(err, "failed to parse file %q", path)
		}

		switch u.GroupVersionKind().Kind {
		case xpv1.CompositeResourceDefinitionKind:
			// Process the XRD and get the paths
			xrPath, claimPath, err := xcrd.ProcessXRD(crdFS, bs, path, baseFolder)
			if err != nil {
				return err
			}

			if xrPath != "" {
				bs, err := afero.ReadFile(crdFS, xrPath)
				if err != nil {
					return errors.Wrapf(err, "failed to read file %q", path)
				}
				if err := appendOpenAPIPath(crdFS, bs, xrPath, baseFolder, &openAPIPaths); err != nil {
					return err
				}
			}
			if claimPath != "" {
				bs, err := afero.ReadFile(crdFS, claimPath)
				if err != nil {
					return errors.Wrapf(err, "failed to read file %q", path)
				}
				if err := appendOpenAPIPath(crdFS, bs, claimPath, baseFolder, &openAPIPaths); err != nil {
					return err
				}
			}

		case "CustomResourceDefinition":
			if err := appendOpenAPIPath(crdFS, bs, path, baseFolder, &openAPIPaths); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if len(openAPIPaths) == 0 {
		// Return nil if no files were generated
		return nil, nil
	}

	if err := generator.Generate(
		ctx,
		crdFS,
		baseFolder,
		"koxudaxi/datamodel-code-generator:0.26.1",
		[]string{
			"--input-file-type",
			"openapi",
			"--disable-timestamp",
			"--input",
			".",
			"--output-model-type",
			"pydantic_v2.BaseModel",
			"--target-python-version",
			"3.12",
			"--use-field-description",
			"--output",
			pythonModelsFolder,
		},
	); err != nil {
		return nil, err
	}

	// reorganization alignment https://github.com/koxudaxi/datamodel-code-generator/issues/2097
	if err := reorganizeAndAdjustImports(crdFS, pythonGeneratedFolder, pythonAdoptModelsStructure); err != nil {
		return nil, err
	}

	// Copy only the files from pythonAdoptModelsStructure into the schemaFs
	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(crdFS, pythonAdoptModelsStructure), afero.NewBasePathFs(schemaFS, pythonModelsFolder)); err != nil {
		return nil, err
	}

	return schemaFS, nil
}

func appendOpenAPIPath(crdFS afero.Fs, bs []byte, path, baseFolder string, openAPIPaths *[]string) error {
	openAPIPath, err := xcrd.ConvertToOpenAPI(crdFS, bs, path, baseFolder)
	if err != nil {
		return err
	}
	*openAPIPaths = append(*openAPIPaths, openAPIPath)
	return nil
}

// reorganizeAndAdjustImports combines the reorganization of Python files and the adjustment of import paths into one pass
func reorganizeAndAdjustImports(fs afero.Fs, sourceDir, targetDir string) error { //nolint:gocyclo
	v1MetaCopied := false // Flag to track if v1.py has already been moved
	createdInitFiles := make(map[string]bool)

	// Walk through the source directory to handle both reorganization and import adjustment
	return afero.Walk(fs, sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "walking path %s", path)
		}

		// If this is the `v1.py` file within `k8s/apimachinery/pkg/apis/meta`, move it once
		if info.Name() == "v1.py" && strings.Contains(path, filepath.Join("io", "k8s", "apimachinery", "pkg", "apis", "meta")) {
			if !v1MetaCopied {
				destDir := filepath.Join(targetDir, "io", "k8s", "apimachinery", "pkg", "apis", "meta")
				destPath := filepath.Join(destDir, "v1.py")

				// Read file content and write it to the new destination
				data, err := afero.ReadFile(fs, path)
				if err != nil {
					return errors.Wrapf(err, "failed to read %s", path)
				}

				// Get the source file's permissions
				fileInfo, err := fs.Stat(path)
				if err != nil {
					return errors.Wrapf(err, "failed to get file info for %s", path)
				}

				// Use the source file's permissions instead of os.ModePerm
				if err := afero.WriteFile(fs, destPath, data, fileInfo.Mode()); err != nil {
					return errors.Wrapf(err, "failed to write %s", destPath)
				}

				// Create __init__.py in the same directory if it doesn't exist
				initFilePath := filepath.Join(destDir, "__init__.py")
				if err := afero.WriteFile(fs, initFilePath, []byte(""), os.ModePerm); err != nil {
					return errors.Wrapf(err, "failed to create __init__.py in %s", destDir)
				}

				v1MetaCopied = true // Ensure we copy it only once
			}
			return nil // Skip subsequent meta v1.py files
		}

		// Process only schema files
		isDir := info.IsDir()
		isNotPythonFile := filepath.Ext(info.Name()) != ".py"
		// Define the path segment to skip
		skipPathSegment := filepath.Join("io", "k8s", "apimachinery", "pkg", "apis", "meta")
		isInSkipPath := strings.Contains(filepath.ToSlash(path), skipPathSegment)
		isInitFile := info.Name() == "__init__.py"

		if isDir || isNotPythonFile || isInSkipPath || isInitFile {
			return nil
		}

		// Process the reorganization logic
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return errors.Wrap(err, "calculating relative path")
		}
		dirSegments := strings.Split(filepath.ToSlash(filepath.Dir(relPath)), "/")

		// Extract API version and segments before it
		var apiVersion, rootFolder string
		var preVersionSegments []string
		for _, dirSegment := range dirSegments {
			subSegments := strings.Split(dirSegment, "_")
			for _, subSegment := range subSegments {
				if xcrd.IsKnownAPIVersion(subSegment) {
					apiVersion = subSegment
					rootFolder = dirSegment
					break
				}
				preVersionSegments = append(preVersionSegments, subSegment)
			}
			if apiVersion != "" {
				break
			}
		}

		// If no known API version is found, default to "unknown"
		if apiVersion == "" || rootFolder == "" {
			apiVersion = "unknown"
		}

		// Build the destination directory
		slices.Reverse(preVersionSegments)
		orderedPath := filepath.Join(preVersionSegments...)
		rootWithoutVersion := strings.ReplaceAll(rootFolder, apiVersion, "")
		rootParts := strings.Split(rootWithoutVersion, "_")
		kind := rootParts[len(rootParts)-1] // Extract the kind

		// Prepare destination path
		newFileName := fmt.Sprintf("%s.py", apiVersion)
		destinationDir := filepath.Join(targetDir, orderedPath, kind)
		destinationPath := filepath.Join(destinationDir, newFileName)

		// Create the destination directory
		if err := fs.MkdirAll(destinationDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "creating directory %s", destinationDir)
		}

		// Read the file content and move it
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return errors.Wrapf(err, "reading file %s", path)
		}
		if err := afero.WriteFile(fs, destinationPath, data, os.ModePerm); err != nil {
			return errors.Wrapf(err, "writing file %s", destinationPath)
		}
		if err := fs.Remove(path); err != nil {
			return errors.Wrapf(err, "deleting original file %s", path)
		}

		// Ensure an __init__.py is created in the destination directory if it doesn't exist
		initFilePath := filepath.Join(destinationDir, "__init__.py")
		if !createdInitFiles[destinationDir] {
			if err := afero.WriteFile(fs, initFilePath, []byte(""), os.ModePerm); err != nil {
				return errors.Wrapf(err, "creating __init__.py in %s", destinationDir)
			}
			createdInitFiles[destinationDir] = true
		}

		// Adjust the imports for the moved file
		if err := adjustImportsInFile(fs, destinationPath); err != nil {
			return errors.Wrapf(err, "adjusting imports in %s", destinationPath)
		}

		return nil
	})
}

// adjustImportsInFile modifies the import statements in the file to ensure correct depth
func adjustImportsInFile(fs afero.Fs, filePath string) error {

	// Count the number of directories deep the file is
	depth := strings.Count(filePath, string(os.PathSeparator))

	// Read the file content
	fileContent, err := afero.ReadFile(fs, filePath)
	if err != nil {
		return errors.Wrapf(err, "error reading file %s", filePath)
	}

	// Modify the file line by line to adjust the specific imports
	modifiedContent := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(fileContent)))
	for scanner.Scan() {
		line := scanner.Text()
		// Adjust imports that contain `io.k8s.apimachinery.pkg.apis.meta`
		if strings.Contains(line, "io.k8s.apimachinery.pkg.apis.meta") {
			line = adjustLeadingDots(line, depth)
		}
		modifiedContent = append(modifiedContent, line)
	}

	// Write back the modified file content
	if err := afero.WriteFile(fs, filePath, []byte(strings.Join(modifiedContent, "\n")), os.ModePerm); err != nil {
		return errors.Wrapf(err, "error writing modified file %s", filePath)
	}

	return nil
}

// Adjusts the number of leading dots in the `io.k8s.apimachinery.pkg.apis.meta` import statement
// based on the file's depth
func adjustLeadingDots(importLine string, depth int) string {
	// Add the correct number of leading dots based on depth
	dotPart := strings.Repeat(".", depth)

	// Find the import statement containing `io.k8s.apimachinery.pkg.apis.meta` and remove any existing leading dots
	if strings.Contains(importLine, "io.k8s.apimachinery.pkg.apis.meta") {
		// Split the line into parts: the leading dots + the import path
		parts := strings.SplitN(importLine, "io.k8s.apimachinery.pkg.apis.meta", 2)

		// Ensure the first part (before `io.k8s.apimachinery.pkg.apis.meta`) is correctly replaced by the calculated dots
		return "from " + dotPart + "io.k8s.apimachinery.pkg.apis.meta" + parts[1]
	}

	return importLine
}
