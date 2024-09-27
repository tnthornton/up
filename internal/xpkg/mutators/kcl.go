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

package mutators

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	xpv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kcl-lang.io/cli/pkg/import/crd"
	crdGen "kcl-lang.io/kcl-openapi/pkg/kube_resource/generator"
	"kcl-lang.io/kcl-openapi/pkg/swagger/generator"
	"sigs.k8s.io/yaml"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	xcrd "github.com/upbound/up/internal/crd"
	"github.com/upbound/up/internal/filesystem"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/parser/kcl"
)

const (
	kclSchemaFolder = "schemas"
	kclModelsFolder = "models"
)

// KclMutator is responsible for generating and adding the KCL layer.
type KclMutator struct {
	sk *kcl.Kparser
}

// NewKclMutator creates a new KclMutator.
func NewKclMutator(sk *kcl.Kparser) *KclMutator {
	return &KclMutator{
		sk: sk,
	}
}

// Mutate generates and adds the KCL layer to the given image and config.
func (m *KclMutator) Mutate(img v1.Image, cfg v1.Config) (v1.Image, v1.Config, error) {
	if m.sk == nil || m.sk.Filesystem == nil {
		return img, cfg, nil // No mutation if KCL parser or filesystem is missing.
	}

	// Initialize the Kparser with the file system, root path, and file mode.
	kclParser := kcl.New(m.sk.Filesystem, "", xpkg.StreamFileMode)

	// Generate the tarball using the Kparser
	kclTarball, err := kclParser.Generate()
	if err != nil {
		return nil, cfg, errors.Wrap(err, "failed to generate KCL tarball")
	}

	// Convert the tarball to a v1.Layer.
	kclLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(kclTarball)), nil
	})
	if err != nil {
		return nil, cfg, errors.Wrap(err, "failed to convert tarball to v1.Layer")
	}

	// Calculate the layer digest.
	layerDigest, err := kclLayer.Digest()
	if err != nil {
		return nil, cfg, errors.Wrap(err, "failed to calculate layer digest")
	}

	// Update the image config with the annotation label.
	labelKey := xpkg.Label(layerDigest.String())
	cfg.Labels[labelKey] = xpkg.SchemaKclAnnotation

	// Append the KCL layer to the image.
	img, err = mutate.AppendLayers(img, kclLayer)
	if err != nil {
		return nil, cfg, errors.Wrap(err, "failed to append KCL layer to image")
	}

	return img, cfg, nil
}

// GenerateSchemaKcl generates KCL schema files from the XRDs and CRDs fromFS
func GenerateSchemaKcl(fromFS afero.Fs, exclude []string) (afero.Fs, error) { //nolint:gocyclo
	// Use os.TempDir() to create a temporary base folder for processing
	// kcl tooling cannot work with an afero mem/fs
	baseFolder, err := os.MkdirTemp(os.TempDir(), xpkg.SchemaKclAnnotation)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(baseFolder); err != nil {
			fmt.Printf("Warning: failed to remove temp folder %q: %v\n", baseFolder, err)
		}
	}()

	var compositePaths []string

	// Walk the virtual filesystem to find and process target files
	err = afero.Walk(fromFS, "/", func(path string, info fs.FileInfo, err error) error {
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
			if err := xcrd.ProcessXRD(bs, path, baseFolder, &compositePaths); err != nil {
				return err
			}
		case "CustomResourceDefinition":
			if err := filesystem.WriteFile(filepath.Join(baseFolder, path), bs, 0o644); err != nil {
				return err
			}
			compositePaths = append(compositePaths, filepath.Join(baseFolder, path))
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// If no composite paths (XRD/CRD) were processed, we proceed without generating or copying files
	if len(compositePaths) > 0 {
		// Generate KCL files from CRD specs
		generateKCLSchemas(baseFolder, compositePaths)

		// Create an in-memory file system and copy the generated files
		toFs := afero.NewMemMapFs()
		err = filesystem.CopyGeneratedFiles(baseFolder, kclSchemaFolder, toFs)
		if err != nil {
			return nil, err
		}

		return toFs, nil
	}

	// Return nil if no files were generated
	return nil, nil
}

// generateKCLSchemas from CRD specs
func generateKCLSchemas(baseFolder string, compositePaths []string) {
	disableLogging()
	defer enableLogging()

	for _, compositeFile := range compositePaths {
		opts := new(generator.GenOpts)
		opts.Spec = compositeFile
		opts.Target = filepath.Join(baseFolder, kclSchemaFolder)
		opts.ModelPackage = filepath.Join(opts.Target, kclModelsFolder)

		if err := opts.EnsureDefaults(); err != nil {
			fmt.Println(errors.Wrap(err, "kcl schema defaults not applied"))
			continue
		}

		specs, err := crdGen.GetSpecs(&crdGen.GenOpts{Spec: opts.Spec})
		if err != nil {
			continue
		}

		for _, spec := range specs {
			opts.Spec = spec
			if err := generator.Generate(opts); err != nil {
				fmt.Println(err)
			}
		}

		err = crd.GroupByKclFiles(opts.ModelPackage)
		if err != nil {
			fmt.Println(errors.Wrap(err, "schema kcl grouping not possible"))
		}
	}
}

func disableLogging() {
	log.SetOutput(io.Discard)
}

func enableLogging() {
	log.SetOutput(os.Stdout)
}
