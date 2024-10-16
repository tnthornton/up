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

package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/upterm"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/mutators"
	"github.com/upbound/up/internal/xpkg/parser/schema"
	"github.com/upbound/up/internal/xpkg/schemagenerator"
	"github.com/upbound/up/internal/xpkg/schemarunner"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	xpkgmarshaler "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
)

type cli struct {
	SourceImage string `help:"The source image to pull." required:""`
	TargetImage string `help:"The target image to push to." required:""`
}

func main() {
	c := cli{}
	kong.Parse(&c)
	pterm.EnableStyling()

	ctx := context.Background()
	if err := c.generateSchema(ctx); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func (c *cli) generateSchema(ctx context.Context) error { // nolint:gocyclo
	var (
		image v1.Image
		err   error
	)

	err = upterm.WrapWithSuccessSpinner(fmt.Sprintf("Loading Source Image %s", c.SourceImage), upterm.CheckmarkSuccessSpinner, func() error {
		image, err = crane.Pull(c.SourceImage)
		return err
	})
	if err != nil {
		return errors.Wrapf(err, "error pulling image")
	}

	configFile, err := image.ConfigFile()
	if err != nil {
		return errors.Wrapf(err, "error getting image config file")
	}

	m, err := xpkgmarshaler.NewMarshaler()
	if err != nil {
		return errors.Wrapf(err, "error getting xpkg marshaler")
	}

	parsedPkg, err := m.FromImage(xpkg.Image{ //nolint:contextcheck
		Image: image,
	})
	if err != nil {
		return errors.Wrapf(err, "error parsing image")
	}

	memFs := afero.NewMemMapFs()
	cerr := copyCrdToFs(parsedPkg, memFs)
	if cerr != nil {
		return errors.Wrapf(err, "error copy crds to fs")
	}

	err = upterm.WrapWithSuccessSpinner("Schema Generation", upterm.CheckmarkSuccessSpinner, func() error {
		image, err = runSchemaGeneration(ctx, memFs, image, configFile.Config)
		return err
	})
	if err != nil {
		return errors.Wrapf(err, "error generating schema")
	}

	err = upterm.WrapWithSuccessSpinner(fmt.Sprintf("Pushing Target Image %s", c.TargetImage), upterm.CheckmarkSuccessSpinner, func() error {
		err = crane.Push(image, c.TargetImage)
		return err
	})
	if err != nil {
		return errors.Wrapf(err, "error push to registry %v", c.TargetImage)
	}

	return nil
}

// copyCrdToFs get Objs from ParsedPackage identifies CRDs, and stores them in FS
func copyCrdToFs(pp *xpkgmarshaler.ParsedPackage, fs afero.Fs) error {
	for i, obj := range pp.Objs {
		crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return errors.New("object is not a CustomResourceDefinition")
		}

		data, err := yaml.Marshal(crd)
		if err != nil {
			return errors.Wrapf(err, "failed to serialize CRD %d", i)
		}

		crdName := fmt.Sprintf("/%s_%s.yaml", crd.Spec.Group, crd.Spec.Names.Plural)
		filePath := filepath.Join(pp.DepName, crdName)

		err = afero.WriteFile(fs, filePath, data, 0644)
		if err != nil {
			return errors.Wrapf(err, "failed to write CRD %d to FS", i)
		}
	}
	return nil
}

// runSchemaGeneration generates the schema and applies mutators to the base configuration
func runSchemaGeneration(ctx context.Context, memFs afero.Fs, image v1.Image, cfg v1.Config) (v1.Image, error) {
	apiExcludes := []string{}
	schemaRunner := schemarunner.RealSchemaRunner{}

	pfs, err := schemagenerator.GenerateSchemaPython(ctx, memFs, apiExcludes, schemaRunner)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate schema")
	}

	kfs, err := schemagenerator.GenerateSchemaKcl(ctx, memFs, apiExcludes, schemaRunner)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate schema")
	}

	var muts []xpkg.Mutator
	if pfs != nil {
		muts = append(muts, mutators.NewSchemaMutator(schema.New(pfs, "", xpkg.StreamFileMode), xpkg.SchemaPythonAnnotation))
	}
	if kfs != nil {
		muts = append(muts, mutators.NewSchemaMutator(schema.New(kfs, "", xpkg.StreamFileMode), xpkg.SchemaKclAnnotation))
	}

	for _, mut := range muts {
		if mut != nil {
			var err error
			image, cfg, err = mut.Mutate(image, cfg)
			if err != nil {
				return nil, errors.Wrap(err, "failed to apply mutator")
			}
		}
	}

	image, err = mutate.Config(image, cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to mutate config for image")
	}

	image, err = xpkg.AnnotateImage(image)
	if err != nil {
		return nil, errors.Wrap(err, "failed to annotate image")
	}

	return image, nil
}
