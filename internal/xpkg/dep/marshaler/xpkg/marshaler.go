// Copyright 2021 Upbound Inc
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

package xpkg

import (
	"archive/tar"
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	cv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	xpmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	xpmetav1beta1 "github.com/crossplane/crossplane/apis/pkg/meta/v1beta1"

	"github.com/crossplane/crossplane-runtime/pkg/parser"
	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/crossplane/crossplane/xcrd"

	"github.com/upbound/up/internal/filesystem"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/parser/linter"
	"github.com/upbound/up/internal/xpkg/parser/ndjson"
	"github.com/upbound/up/internal/xpkg/parser/yaml"
	"github.com/upbound/up/internal/xpkg/scheme"
)

const (
	errFailedToParsePkgYaml         = "failed to parse package yaml"
	errLintPackage                  = "failed to lint package"
	errOpenPackageStream            = "failed to open package stream file"
	errConvertXRDs                  = "failed to convert XRD to CRD"
	errFailedToConvertMetaToPackage = "failed to convert meta to package"
	errInvalidPath                  = "invalid path provided for package lookup"
	errNotExactlyOneMeta            = "not exactly one package meta type"
	maxFileSize                     = 1024 * 1024 * 1024
)

var (
	crdGVK = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
)

// Marshaler represents a xpkg Marshaler
type Marshaler struct {
	yp parser.Parser
	jp JSONPackageParser
}

// NewMarshaler returns a new Marshaler
func NewMarshaler(opts ...MarshalerOption) (*Marshaler, error) {
	r := &Marshaler{}
	yp, err := yaml.New()
	if err != nil {
		return nil, err
	}

	jp, err := ndjson.New()
	if err != nil {
		return nil, err
	}

	r.yp = yp
	r.jp = jp

	for _, o := range opts {
		o(r)
	}

	return r, nil
}

// MarshalerOption modifies the xpkg Marshaler
type MarshalerOption func(*Marshaler)

// WithYamlParser modifies the Marshaler by setting the supplied PackageParser as
// the Resolver's parser.
func WithYamlParser(p parser.Parser) MarshalerOption {
	return func(r *Marshaler) {
		r.yp = p
	}
}

// WithJSONParser modifies the Marshaler by setting the supplied PackageParser as
// the Resolver's parser.
func WithJSONParser(p JSONPackageParser) MarshalerOption {
	return func(r *Marshaler) {
		r.jp = p
	}
}

// FromImage takes a xpkg.Image and returns a ParsedPackage for consumption by
// upstream callers.
func (r *Marshaler) FromImage(i xpkg.Image) (*ParsedPackage, error) { // nolint:gocyclo
	manifest, err := i.Image.Manifest()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image manifest")
	}

	var packageLayerDigest cv1.Hash
	var schemaFS = make(map[string]afero.Fs)

	for _, l := range manifest.Layers {
		if val, ok := l.Annotations[xpkg.AnnotationKey]; ok && val == xpkg.PackageAnnotation {
			packageLayerDigest = l.Digest
		}

		// Dynamically detect schema annotations (e.g., schema.python, schema.kcl, etc.)
		for _, annotationValue := range l.Annotations {
			if strings.HasPrefix(annotationValue, "schema.") {
				schemaType := strings.TrimPrefix(annotationValue, "schema.")
				schemaMemFs := afero.NewMemMapFs()
				err := extractLayerToFs(i, l.Digest, schemaMemFs)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to extract %s schema layer", schemaType)
				}
				schemaFS[schemaType] = schemaMemFs
			}
		}
	}

	if packageLayerDigest.String() == "" {
		return nil, errors.New("package layer with specified annotation not found")
	}

	packageLayer, err := i.Image.LayerByDigest(packageLayerDigest)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find the package layer")
	}

	reader, err := packageLayer.Uncompressed()
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract package layer")
	}

	fs := tarfs.New(tar.NewReader(reader))
	pkgYaml, err := fs.Open(xpkg.StreamFile)
	if err != nil {
		return nil, errors.Wrap(err, errOpenPackageStream)
	}

	pkg, err := r.parseYaml(pkgYaml)
	if err != nil {
		return nil, err
	}

	pkg = applyImageMeta(i.Meta, pkg)

	if pkg, err = convertXRD2CRD(pkg); err != nil {
		return nil, errors.Wrap(err, errConvertXRDs)
	}

	pkg.Schema = schemaFS
	return finalizePkg(pkg)
}

// FromDir takes an afero.Fs and a path to a directory and returns a
// ParsedPackage based on the directories contents for consumption by upstream
// callers.
func (r *Marshaler) FromDir(fs afero.Fs, path string) (*ParsedPackage, error) {
	parts := strings.Split(path, "@")
	if len(parts) != 2 {
		return nil, errors.New(errInvalidPath)
	}

	pkgJSON, err := fs.Open(filepath.Join(path, xpkg.JSONStreamFile))
	if err != nil {
		return nil, err
	}

	pkg, err := r.parseNDJSON(pkgJSON)
	if err != nil {
		return nil, err
	}

	// Find all schema.* directories and save in aferoFS
	schemaFS, err := r.loadSchemasFromDir(fs, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load schema directories")
	}

	pkg.Schema = schemaFS

	return finalizePkg(pkg)
}

// parseYaml parses the
func (r *Marshaler) parseYaml(reader io.ReadCloser) (*ParsedPackage, error) {
	pkg, err := r.yp.Parse(context.Background(), reader)
	if err != nil {
		return nil, errors.Wrap(err, errFailedToParsePkgYaml)
	}
	return processPackage(pkg)
}

func processPackage(pkg linter.Package) (*ParsedPackage, error) {
	metas := pkg.GetMeta()
	if len(metas) != 1 {
		return nil, errors.New(errNotExactlyOneMeta)
	}

	meta := metas[0]
	var linter linter.Linter
	var pkgType v1beta1.PackageType
	switch meta.GetObjectKind().GroupVersionKind().Kind {
	case xpmetav1.ConfigurationKind:
		linter = xpkg.NewConfigurationLinter()
		pkgType = v1beta1.ConfigurationPackageType
	case xpmetav1.ProviderKind:
		linter = xpkg.NewProviderLinter()
		pkgType = v1beta1.ProviderPackageType
	case xpmetav1beta1.FunctionKind:
		linter = xpkg.NewFunctionLinter()
		pkgType = v1beta1.FunctionPackageType
	}
	if err := linter.Lint(pkg); err != nil {
		return nil, errors.Wrap(err, errLintPackage)
	}

	return &ParsedPackage{
		MetaObj: meta,
		Objs:    pkg.GetObjects(),
		PType:   pkgType,
	}, nil
}

func (r *Marshaler) parseNDJSON(reader io.ReadCloser) (*ParsedPackage, error) {
	pkg, err := r.jp.Parse(context.Background(), reader)
	if err != nil {
		return nil, errors.Wrap(err, errFailedToParsePkgYaml)
	}

	metas := pkg.GetMeta()
	if len(metas) != 1 {
		return nil, errors.New(errNotExactlyOneMeta)
	}

	meta := metas[0]

	// Check if the meta kind is ConfigurationKind
	if meta.GetObjectKind().GroupVersionKind().Kind == xpmetav1.ConfigurationKind {
		filteredObjects := []runtime.Object{}
		for _, obj := range pkg.GetObjects() {
			// Only include objects of type CompositeResourceDefinition or Composition
			if _, isXRD := obj.(*v1.CompositeResourceDefinition); isXRD {
				filteredObjects = append(filteredObjects, obj)
			} else if _, isComposition := obj.(*v1.Composition); isComposition {
				filteredObjects = append(filteredObjects, obj)
			}
		}
		// Replace pkg.objects with the filtered list
		pkg.SetObjects(filteredObjects)
	}

	p, err := processPackage(pkg)
	if err != nil {
		return nil, err
	}

	return applyImageMeta(pkg.GetImageMeta(), p), nil
}

func applyImageMeta(m xpkg.ImageMeta, pkg *ParsedPackage) *ParsedPackage {
	pkg.DepName = m.Repo
	pkg.Reg = m.Registry
	pkg.SHA = m.Digest
	pkg.Ver = m.Version

	return pkg
}

func convertXRD2CRD(pkg *ParsedPackage) (*ParsedPackage, error) {
	for _, obj := range pkg.Objects() {
		if obj.GetObjectKind().GroupVersionKind().Kind == "CompositeResourceDefinition" {
			xrd := obj.(*v1.CompositeResourceDefinition)

			crd, err := xcrd.ForCompositeResource(obj.(*v1.CompositeResourceDefinition))
			if err != nil {
				return nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q", xrd.GetName())
			}
			crd.SetGroupVersionKind(crdGVK)
			pkg.Objs = append(pkg.Objs, crd)

			if xrd.Spec.ClaimNames != nil {
				claimCrd, err := xcrd.ForCompositeResourceClaim(obj.(*v1.CompositeResourceDefinition))
				if err != nil {
					return nil, errors.Wrapf(err, "cannot derive claim CRD from XRD %q", xrd.GetName())
				}
				claimCrd.SetGroupVersionKind(crdGVK)
				pkg.Objs = append(pkg.Objs, claimCrd)
			}
		}
	}

	return pkg, nil
}

func finalizePkg(pkg *ParsedPackage) (*ParsedPackage, error) { // nolint:gocyclo
	deps, err := determineDeps(pkg.MetaObj)
	if err != nil {
		return nil, err
	}

	pkg.Deps = deps

	return pkg, nil
}

func determineDeps(o runtime.Object) ([]v1beta1.Dependency, error) {
	pkg, ok := scheme.TryConvertToPkg(o, &xpmetav1.Provider{}, &xpmetav1.Configuration{}, &xpmetav1.Function{})
	if !ok {
		return nil, errors.New(errFailedToConvertMetaToPackage)
	}

	out := make([]v1beta1.Dependency, len(pkg.GetDependencies()))
	for i, d := range pkg.GetDependencies() {
		out[i] = convertToV1beta1(d)
	}

	return out, nil
}

func convertToV1beta1(in xpmetav1.Dependency) v1beta1.Dependency {
	betaD := v1beta1.Dependency{
		Constraints: in.Version,
	}

	if in.Provider != nil {
		betaD.Package = *in.Provider
		betaD.Type = v1beta1.ProviderPackageType
	}

	if in.Configuration != nil {
		betaD.Package = *in.Configuration
		betaD.Type = v1beta1.ConfigurationPackageType
	}

	if in.Function != nil {
		betaD.Package = *in.Function
		betaD.Type = v1beta1.FunctionPackageType
	}

	return betaD
}

func extractLayerToFs(i xpkg.Image, layerDigest cv1.Hash, fs afero.Fs) error { // nolint:gocyclo
	layers, err := i.Image.Layers()
	if err != nil {
		return errors.Wrap(err, "failed to get image layers")
	}

	var targetLayer cv1.Layer
	for _, l := range layers {
		h, err := l.Digest()
		if err != nil {
			return errors.Wrap(err, "failed to get layer digest")
		}

		if h == layerDigest {
			targetLayer = l
			break
		}
	}

	if targetLayer == nil {
		return errors.New("failed to find the target layer")
	}

	reader, err := targetLayer.Uncompressed()
	if err != nil {
		return errors.Wrap(err, "failed to extract target layer")
	}

	tarReader := tar.NewReader(reader)

	// Iterate over the files in the tarball and write them to the Afero filesystem
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return errors.Wrap(err, "failed to read tar header")
		}

		// Construct the full output path for the file in the Afero filesystem
		outputPath := header.Name

		switch header.Typeflag {
		case tar.TypeDir:
			// Create directories in the Afero filesystem
			if err := fs.MkdirAll(outputPath, 0755); err != nil {
				return errors.Wrap(err, "failed to create directory in afero fs")
			}
		case tar.TypeReg:
			// Create regular files in the Afero filesystem
			outFile, err := fs.Create(outputPath)
			if err != nil {
				return errors.Wrap(err, "failed to create file in afero fs")
			}
			defer func() {}()

			// Limit the number of bytes copied from the tarReader to prevent decompression bombs
			limitedReader := io.LimitReader(tarReader, maxFileSize)

			// Copy the contents of the tar file to the newly created file with size limit
			if _, err := io.Copy(outFile, limitedReader); err != nil {
				return errors.Wrap(err, "failed to write file in afero fs or exceeded file size limit")
			}

		}
	}

	return nil
}

func (r *Marshaler) loadSchemasFromDir(fs afero.Fs, path string) (map[string]afero.Fs, error) {
	schemaFS := make(map[string]afero.Fs)

	// Read the contents of the directory
	dirEntries, err := afero.ReadDir(fs, path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read directory")
	}

	// Iterate through the directory contents to find schema.* folders
	for _, entry := range dirEntries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "schema.") {
			// Extract the schema type (e.g., "python" from "schema.python")
			schemaType := strings.TrimPrefix(entry.Name(), "schema.")

			// Create an in-memory filesystem for this schema
			schemaMemFS := afero.NewMemMapFs()
			sourceFS := afero.NewBasePathFs(fs, filepath.Join(path, entry.Name()))

			// Read the contents of the schema directory and copy to memFS
			err := filesystem.CopyFilesBetweenFs(sourceFS, schemaMemFS)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to copy schema directory %s", entry.Name())
			}

			// Add the memFS to the schemaFS map
			schemaFS[schemaType] = schemaMemFS
		}
	}

	return schemaFS, nil
}
