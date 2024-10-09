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

package functions

import (
	"archive/tar"
	"context"
	"embed"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/upbound/up/internal/xpkg"
)

func TestIdentify(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		files           map[string]string
		expectError     bool
		expectedBuilder Builder
	}{
		"DockerfileOnly": {
			files: map[string]string{
				"Dockerfile": "FROM scratch",
			},
			expectedBuilder: &dockerBuilder{},
		},
		"KCLOnly": {
			files: map[string]string{
				"kcl.mod": "[package]",
			},
			expectedBuilder: &kclBuilder{},
		},
		"DockerfileAndKCL": {
			files: map[string]string{
				"Dockerfile": "FROM scratch",
				"kcl.mod":    "[package]",
			},
			// dockerBuilder has precedence.
			expectedBuilder: &dockerBuilder{},
		},
		"Empty": {
			files:       make(map[string]string),
			expectError: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fromFS := afero.NewMemMapFs()
			for fname, content := range tc.files {
				err := afero.WriteFile(fromFS, fname, []byte(content), 0644)
				assert.NilError(t, err)
			}

			builder, err := DefaultIdentifier.Identify(fromFS)
			if tc.expectError {
				assert.Error(t, err, errNoSuitableBuilder)
			} else {
				wantType := reflect.TypeOf(tc.expectedBuilder)
				gotType := reflect.TypeOf(builder)
				assert.Equal(t, wantType, gotType)
			}
		})
	}
}

//go:embed testdata/kcl-function/**
var kclFunction embed.FS

func TestKCLBuild(t *testing.T) {
	t.Parallel()

	// Start a test registry to serve the base image.
	regSrv, err := registry.TLS("localhost")
	assert.NilError(t, err)
	t.Cleanup(regSrv.Close)
	testRegistry, err := name.NewRegistry(strings.TrimPrefix(regSrv.URL, "https://"))
	assert.NilError(t, err)

	// Put an base image in the registry that contains only a package layer. The
	// package layer should be removed when we build a function on top of it.
	baseImageRef := testRegistry.Repo("unittest-base-image").Tag("latest")
	baseImage, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: "amd64",
	})
	assert.NilError(t, err)
	baseLayer, err := random.Layer(1, types.OCILayer)
	assert.NilError(t, err)
	baseImage, err = mutate.Append(baseImage, mutate.Addendum{
		Layer: baseLayer,
		Annotations: map[string]string{
			xpkg.AnnotationKey: xpkg.PackageAnnotation,
		},
	})
	assert.NilError(t, err)
	err = remote.Put(baseImageRef, baseImage, remote.WithTransport(regSrv.Client().Transport))
	assert.NilError(t, err)

	// Build a KCL function on top of the base image.
	b := &kclBuilder{
		baseImage: baseImageRef.String(),
		transport: regSrv.Client().Transport,
	}
	fromFS := afero.NewBasePathFs(
		afero.FromIOFS{FS: kclFunction},
		"testdata/kcl-function",
	)
	fnImgs, err := b.Build(context.Background(), fromFS, []string{"amd64"})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(fnImgs, 1))
	fnImg := fnImgs[0]

	// Ensure the default source was set correctly.
	cfgFile, err := fnImg.ConfigFile()
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(cfgFile.Config.Env, "FUNCTION_KCL_DEFAULT_SOURCE=/src"))

	// Verify that the code layer was added.
	layers, err := fnImg.Layers()
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(layers, 1))
	layer := layers[0]
	rc, err := layer.Uncompressed()
	assert.NilError(t, err)

	// Make sure all the files got added with the correct contents.
	tr := tar.NewReader(rc)
	tfs := tarfs.New(tr)
	_ = afero.Walk(fromFS, "/", func(path string, info fs.FileInfo, err error) error {
		assert.NilError(t, err)

		tpath := filepath.Join("/src", path)
		st, err := tfs.Stat(tpath)
		assert.NilError(t, err)

		if st.IsDir() {
			return nil
		}
		wantContents, err := afero.ReadFile(fromFS, path)
		assert.NilError(t, err)
		gotContents, err := afero.ReadFile(tfs, tpath)
		assert.NilError(t, err)
		assert.DeepEqual(t, wantContents, gotContents)

		return nil
	})
	assert.NilError(t, err)
}
