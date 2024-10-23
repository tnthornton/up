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
	"embed"
	"fmt"
	"os"
	"strings"
	"testing"

	v1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up/internal/filesystem"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"

	"gotest.tools/v3/assert"
)

var (
	//go:embed testdata/project-embedded-functions/**
	projectEmbeddedFunctions embed.FS

	//go:embed testdata/packages/*
	packagesFS embed.FS
)

// TestGenerateCmd_Run tests the Run method of the generateCmd struct.
func TestGenerateCmd_Run(t *testing.T) {
	tcs := map[string]struct {
		language        string
		compositionPath string
		expectedFiles   []string
		err             error
	}{
		"LanguageKcl": {
			language:        "kcl",
			compositionPath: "apis/primitives/XNetwork/composition.yaml",
			expectedFiles:   []string{"main.k", "kcl.mod", "kcl.mod.lock"},
			err:             nil,
		},
		"LanguagePython": {
			language:        "python",
			compositionPath: "apis/primitives/XNetwork/composition.yaml",
			expectedFiles:   []string{"main.py", "requirements.txt"},
			err:             nil,
		},
	}

	for testName, tc := range tcs {
		t.Run(testName, func(t *testing.T) {
			outFS := afero.NewMemMapFs()
			tempProjDir, err := afero.TempDir(afero.NewOsFs(), os.TempDir(), "projFS")
			assert.NilError(t, err)
			defer os.RemoveAll(tempProjDir)

			projFS := afero.NewBasePathFs(afero.NewOsFs(), tempProjDir)
			srcFS := afero.NewBasePathFs(afero.FromIOFS{FS: projectEmbeddedFunctions}, "testdata/project-embedded-functions")

			err = filesystem.CopyFilesBetweenFs(srcFS, projFS)
			assert.NilError(t, err)

			ws, err := workspace.New("/", workspace.WithFS(outFS), workspace.WithPermissiveParser())
			assert.NilError(t, err)
			err = ws.Parse(context.Background())
			assert.NilError(t, err)

			cch, err := cache.NewLocal("/cache", cache.WithFS(outFS))
			assert.NilError(t, err)

			// Create mock fetcher that holds the images
			testPkgFS := afero.NewBasePathFs(afero.FromIOFS{FS: packagesFS}, "testdata/packages")

			r := image.NewResolver(
				image.WithFetcher(
					&image.FSFetcher{FS: testPkgFS},
				),
			)

			mgr, err := manager.New(
				manager.WithCache(cch),
				manager.WithResolver(r),
			)
			assert.NilError(t, err)

			// Construct a workspace from the test filesystem
			ws, err = workspace.New("/",
				workspace.WithFS(projFS), // Use the copied projFS here
				workspace.WithPermissiveParser(),
			)
			assert.NilError(t, err)
			err = ws.Parse(context.Background())
			assert.NilError(t, err)

			// Create temporary directories for the functionFS
			tempFunctionDir, err := afero.TempDir(afero.NewOsFs(), "", "functionFS")
			assert.NilError(t, err)
			defer os.RemoveAll(tempFunctionDir) // Clean up after the test

			// Use BasePathFs for functionFS, scoped to the temp directories
			functionFS := afero.NewBasePathFs(afero.NewOsFs(), tempFunctionDir)

			// Setup the generateCmd with mock dependencies
			c := &generateCmd{
				ProjectFile:       "upbound.yaml",
				projFS:            projFS,
				functionFS:        functionFS,
				Language:          tc.language,
				CompositionPath:   tc.compositionPath,
				projectRepository: "xpkg.upbound.io/awg/getting-started",
				m:                 mgr,
				ws:                ws,
			}

			err = c.Run(context.Background(), &pterm.BasicTextPrinter{
				Style:  pterm.DefaultBasicText.Style,
				Writer: &TestWriter{t},
			})

			if tc.err == nil {
				assert.NilError(t, err)
			} else {
				assert.Assert(t, strings.Contains(err.Error(), tc.err.Error()), "expected error message does not match")
			}

			compYAML, err := afero.ReadFile(projFS, tc.compositionPath)
			assert.NilError(t, err)

			var comp v1.Composition
			err = yaml.Unmarshal(compYAML, &comp)
			assert.NilError(t, err)

			if len(comp.Spec.Pipeline) > 0 {
				step := comp.Spec.Pipeline[0]
				fnRepo := fmt.Sprintf("%s_%s", c.projectRepository, strings.ToLower(comp.Spec.CompositeTypeRef.Kind))
				ref, _ := name.ParseReference(fnRepo)
				assert.Equal(t, step.Step, "compose", "expected 'compose' step at index 0")
				assert.Equal(t, step.FunctionRef.Name, xpkg.ToDNSLabel(ref.Context().RepositoryStr()), "unexpected function reference in 'compose' step")
			} else {
				t.Error("expected at least one pipeline step, but found none")
			}

			for _, expectedFile := range tc.expectedFiles {
				exists, err := afero.Exists(functionFS, expectedFile)
				assert.NilError(t, err)
				assert.Assert(t, exists, "file %s not found in functionFS", expectedFile)
			}

			if tc.err == nil {
				assert.NilError(t, err)
			} else {
				assert.Assert(t, strings.Contains(err.Error(), tc.err.Error()), "expected error message does not match")
			}
		})
	}
}

type TestWriter struct {
	t *testing.T
}

func (w *TestWriter) Write(b []byte) (int, error) {
	out := strings.TrimRight(string(b), "\n")
	w.t.Log(out)
	return len(b), nil
}
