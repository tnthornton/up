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
	"context"
	"io/fs"
	"path/filepath"
	"strings"

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
	kclSchemaFolder = "schemas"
	kclModelsFolder = "models"
	kclImage        = "kcllang/kcl:v0.10.6"
)

// GenerateSchemaKcl generates KCL schema files from the XRDs and CRDs fromFS
func GenerateSchemaKcl(ctx context.Context, fromFS afero.Fs, exclude []string, generator schemarunner.SchemaRunner) (afero.Fs, error) { //nolint:gocyclo
	crdFS := afero.NewMemMapFs()
	schemaFS := afero.NewMemMapFs()
	baseFolder := "workdir"

	if err := crdFS.MkdirAll(baseFolder, 0755); err != nil {
		return nil, err
	}

	var crdPaths []string

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

			// Append paths if they are returned
			if xrPath != "" {
				crdPaths = append(crdPaths, xrPath)
			}
			if claimPath != "" {
				crdPaths = append(crdPaths, claimPath)
			}

		case "CustomResourceDefinition":
			// Write CRD file
			if err := afero.WriteFile(crdFS, filepath.Join(baseFolder, path), bs, 0o644); err != nil {
				return err
			}
			crdPaths = append(crdPaths, filepath.Join(baseFolder, path))
		}

		return nil
	}); err != nil {
		return nil, err
	}

	if len(crdPaths) == 0 {
		// Return nil if no files were generated
		return nil, nil
	}

	if err := generator.Generate(
		ctx,
		crdFS,
		baseFolder,
		kclImage,
		[]string{
			"sh", "-c",
			`find . -name "*.yaml" -exec kcl import -m crd -s {} \;`,
		},
	); err != nil {
		return nil, err
	}

	// Copy only the files from kclModelsFolder into the schemaFs
	if err := filesystem.CopyFilesBetweenFs(afero.NewBasePathFs(crdFS, kclModelsFolder), afero.NewBasePathFs(schemaFS, kclModelsFolder)); err != nil {
		return nil, err
	}

	return schemaFS, nil
}
