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

//go:build integration
// +build integration

package schemagenerator

import (
	"archive/tar"
	"context"
	"embed"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/crossplane/crossplane-runtime/pkg/parser"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"

	"github.com/upbound/up/internal/xpkg"
	umutators "github.com/upbound/up/internal/xpkg/mutators"
	"github.com/upbound/up/internal/xpkg/parser/schema"
	"github.com/upbound/up/internal/xpkg/parser/yaml"
	"github.com/upbound/up/internal/xpkg/schemarunner"
)

//go:embed testdata/kcl/models/*
var testSchemasKcl embed.FS

//go:embed testdata/python/models/*
var testSchemasPython embed.FS

var (
	testSchemaXrd           []byte
	testSchemaComposition   []byte
	testSchemaConfiguration []byte

	_ parser.Backend = &MockBackend{}
)

func init() {
	testSchemaXrd, _ = afero.ReadFile(afero.NewOsFs(), "testdata/account_scaffold_definition.yaml")
	testSchemaComposition, _ = afero.ReadFile(afero.NewOsFs(), "testdata/account_scaffold_composition.yaml")
	testSchemaConfiguration, _ = afero.ReadFile(afero.NewOsFs(), "testdata/configuration_crossplane.yaml")
}

type MockBackend struct {
	MockInit func() (io.ReadCloser, error)
}

func NewMockInitFn(r io.ReadCloser, err error) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return r, err }
}

func (m *MockBackend) Init(_ context.Context, _ ...parser.BackendOption) (io.ReadCloser, error) {
	return m.MockInit()
}

var _ parser.Parser = &MockParser{}

type MockParser struct {
	MockParse func() (*parser.Package, error)
}

func NewMockParseFn(pkg *parser.Package, err error) func() (*parser.Package, error) {
	return func() (*parser.Package, error) { return pkg, err }
}

func (m *MockParser) Parse(context.Context, io.ReadCloser) (*parser.Package, error) {
	return m.MockParse()
}

func TestSchemas(t *testing.T) {
	// Initialize the YAML parser
	pkgp, _ := yaml.New()

	// Define the function type for file system creation
	type withFsFn func() afero.Fs

	// Define the function type for mutator creation
	type withMutatorsFn func(afero.Fs) []xpkg.Mutator

	// Define the arguments for the test case
	type args struct {
		rootDir  string
		fs       withFsFn
		mutators withMutatorsFn // Add mutators to the test arguments
	}

	// Define the expected output (want)
	type want struct {
		pkgExists      bool
		kclSchemaError error
		labels         []string
		err            error
		requiredFiles  []string // Add requiredFiles to the want struct
	}

	// Define the test cases
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"KCLSchemas": {
			reason: "Should successfully build and have a kcl layer.",
			args: args{
				rootDir: "/",
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = fs.Mkdir("/ws", os.ModePerm)
					_ = fs.Mkdir("/ws/apis", os.ModePerm)
					_ = afero.WriteFile(fs, "/ws/crossplane.yaml", testSchemaConfiguration, os.ModePerm)
					_ = afero.WriteFile(fs, "/ws/apis/composition.yaml", testSchemaComposition, os.ModePerm)
					_ = afero.WriteFile(fs, "/ws/apis/definition.yaml", testSchemaXrd, os.ModePerm)
					return fs
				},
				mutators: func(fs afero.Fs) []xpkg.Mutator {
					var mutators []xpkg.Mutator

					// Generate schemaKclFS
					ctx := context.Background()
					schemaKclFS, err := GenerateSchemaKcl(ctx, fs, []string{}, schemarunner.RealSchemaRunner{})
					if err != nil {
						t.Fatalf("Failed to generate schemaKclFS: %v", err)
					}

					// If schemaKclFS is generated, append the Schema mutator
					if schemaKclFS != nil {
						mutators = append(mutators, umutators.NewSchemaMutator(schema.New(schemaKclFS, "", xpkg.StreamFileMode), xpkg.SchemaKclAnnotation))
					}

					return mutators
				},
			},
			want: want{
				pkgExists:      true,
				kclSchemaError: nil,
				labels: []string{
					xpkg.PackageAnnotation,
					xpkg.SchemaKclAnnotation,
				},
				err: nil,
				requiredFiles: []string{
					"/models/co/acme/platform/v1alpha1/accountscaffold.k",
					"/models/co/acme/platform/v1alpha1/xaccountscaffold.k",
					"/models/k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k",
					"/models/k8s/apimachinery/pkg/apis/meta/v1/object_meta.k",
					"/models/k8s/apimachinery/pkg/apis/meta/v1/owner_reference.k",
					"/models/kcl.mod",
				},
			},
		},
		"PythonSchemas": {
			reason: "Should successfully build and have a python layer.",
			args: args{
				rootDir: "/",
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()
					_ = fs.Mkdir("/ws", os.ModePerm)
					_ = fs.Mkdir("/ws/apis", os.ModePerm)
					_ = afero.WriteFile(fs, "/ws/crossplane.yaml", testSchemaConfiguration, os.ModePerm)
					_ = afero.WriteFile(fs, "/ws/apis/composition.yaml", testSchemaComposition, os.ModePerm)
					_ = afero.WriteFile(fs, "/ws/apis/definition.yaml", testSchemaXrd, os.ModePerm)
					return fs
				},
				mutators: func(fs afero.Fs) []xpkg.Mutator {
					var mutators []xpkg.Mutator

					// Generate schemaPythonFS
					ctx := context.Background()
					schemaPythonFS, err := GenerateSchemaPython(ctx, fs, []string{}, schemarunner.RealSchemaRunner{})
					if err != nil {
						t.Fatalf("Failed to generate schemaPythonFS: %v", err)
					}

					// If schemaPythonFS is generated, append the Schema mutator
					if schemaPythonFS != nil {
						mutators = append(mutators, umutators.NewSchemaMutator(schema.New(schemaPythonFS, "", xpkg.StreamFileMode), xpkg.SchemaPythonAnnotation))
					}

					return mutators
				},
			},
			want: want{
				pkgExists: true,
				labels: []string{
					xpkg.PackageAnnotation,
					xpkg.SchemaPythonAnnotation,
				},
				err: nil,
				requiredFiles: []string{
					"/models/co/acme/platform/accountscaffold/__init__.py",
					"/models/co/acme/platform/accountscaffold/v1alpha1.py",
					"/models/co/acme/platform/xaccountscaffold/__init__.py",
					"/models/co/acme/platform/xaccountscaffold/v1alpha1.py",
					"/models/io/k8s/apimachinery/pkg/apis/meta/__init__.py",
					"/models/io/k8s/apimachinery/pkg/apis/meta/v1.py",
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Initialize the in-memory file system from the test case
			fs := tc.args.fs()

			// Create package backend
			pkgBe := parser.NewFsBackend(
				fs,
				parser.FsDir(tc.args.rootDir),
				parser.FsFilters([]parser.FilterFn{
					parser.SkipDirs(),
					parser.SkipNotYAML(),
					parser.SkipEmpty(),
				}...),
			)

			// Create examples backend (we reintroduce this as part of the builder)
			pkgEx := parser.NewFsBackend(
				fs,
				parser.FsDir(tc.args.rootDir+"/examples"), // Assuming examples are at /ws/examples
				parser.FsFilters([]parser.FilterFn{
					parser.SkipDirs(),
					parser.SkipNotYAML(),
					parser.SkipEmpty(),
				}...),
			)

			// Get the mutators
			mutators := tc.args.mutators(fs)

			// Initialize the builder with schemaKclFS, valid backends, and mutators
			builder := xpkg.New(pkgBe, nil, pkgEx, pkgp, nil, mutators...)

			img, _, err := builder.Build(context.TODO())

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nBuildKclSchemas(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			contents, _ := readImg(img)
			sort.Strings(contents.labels)

			if diff := cmp.Diff(tc.want.pkgExists, len(contents.pkgBytes) != 0); diff != "" {
				t.Errorf("\n%s\nBuildKclSchemas(...): -want package, +got no package:\n%s", tc.reason, diff)
			}

			// Check for file presence in tarFS based on requiredFiles in the want struct
			tarReader := tar.NewReader(mutate.Extract(img))
			tarFs := tarfs.New(tarReader)

			for _, file := range tc.want.requiredFiles {
				_, err := tarFs.Open(file)
				if err != nil {
					t.Errorf("Expected file %s to be present in tar archive, but it was not found", file)
				}
			}

			if diff := cmp.Diff(tc.want.labels, contents.labels, cmpopts.SortSlices(func(i, j int) bool {
				return contents.labels[i] < contents.labels[j]
			})); diff != "" {
				t.Errorf("\n%s\nBuildKclSchemas(...): -want labels, +got labels:\n%s", tc.reason, diff)
			}
		})
	}
}

type xpkgContents struct {
	labels       []string
	pkgBytes     []byte
	exBytes      []byte
	includesAuth bool
}

func readImg(i v1.Image) (xpkgContents, error) {
	contents := xpkgContents{
		labels: make([]string, 0),
	}

	reader := mutate.Extract(i)
	fs := tarfs.New(tar.NewReader(reader))

	pkgYaml, err := fs.Open(xpkg.StreamFile)
	if err != nil {
		return contents, err
	}

	pkgBytes, err := io.ReadAll(pkgYaml)
	if err != nil {
		return contents, err
	}
	contents.pkgBytes = pkgBytes
	ps := string(pkgBytes)

	// This is pretty unfortunate. Unless we build out steps to re-parse the
	// package from the image (i.e. the system under test) we're left
	// performing string parsing. For now we choose part of the auth spec,
	// specifically the version and date used in auth yamls.
	if strings.Contains(ps, xpkg.AuthObjectAnno) {
		contents.includesAuth = strings.Contains(ps, "version: \"2023-06-23\"")
	}

	exYaml, err := fs.Open(xpkg.XpkgExamplesFile)
	if err != nil && !os.IsNotExist(err) {
		return contents, err
	}

	if exYaml != nil {
		exBytes, err := io.ReadAll(exYaml)
		if err != nil {
			return contents, err
		}
		contents.exBytes = exBytes
	}

	labels, err := allLabels(i)
	if err != nil {
		return contents, err
	}

	contents.labels = labels

	return contents, nil
}

func allLabels(i partial.WithConfigFile) ([]string, error) {
	labels := []string{}

	cfgFile, err := i.ConfigFile()
	if err != nil {
		return labels, err
	}

	cfg := cfgFile.Config

	for _, label := range cfg.Labels {
		labels = append(labels, label)
	}

	return labels, nil
}
