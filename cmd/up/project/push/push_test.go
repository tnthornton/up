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

package push

import (
	"context"
	"embed"
	"net/http"
	"net/url"
	"strings"
	"testing"

	xpmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/xpkg"
	xpkgmarshaler "github.com/upbound/up/internal/xpkg/dep/marshaler/xpkg"
)

//go:embed testdata/demo-project/**
var demoProject embed.FS

//go:embed testdata/embedded-functions/**
var embeddedFunctions embed.FS

func TestPush(t *testing.T) {
	// Start a test registry so we can actually do a push.
	regSrv, err := registry.TLS("localhost")
	assert.NilError(t, err)
	t.Cleanup(regSrv.Close)
	testRegistry, err := name.NewRegistry(strings.TrimPrefix(regSrv.URL, "https://"))
	assert.NilError(t, err)

	tcs := map[string]struct {
		projFS                afero.Fs
		repo                  string
		expectedFunctionCount int
	}{
		"ConfigurationOnly": {
			projFS:                afero.NewBasePathFs(afero.FromIOFS{FS: demoProject}, "testdata/demo-project"),
			repo:                  "xpkg.upbound.io/unittest/demo-project",
			expectedFunctionCount: 0,
		},
		"EmbeddedFunctions": {
			projFS:                afero.NewBasePathFs(afero.FromIOFS{FS: embeddedFunctions}, "testdata/embedded-functions"),
			repo:                  "xpkg.upbound.io/unittest/embedded-functions",
			expectedFunctionCount: 1,
		},
	}

	for testName, tc := range tcs {
		// Pin loop vars.
		testName, tc := testName, tc
		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			repo, err := name.NewRepository(tc.repo)
			assert.NilError(t, err)

			transport := &hostReplacementRoundTripper{
				wrap: regSrv.Client().Transport,
				from: "xpkg.upbound.io",
				to:   testRegistry.RegistryStr(),
			}
			c := &Cmd{
				ProjectFile: "upbound.yaml",
				Repository:  tc.repo,
				Tag:         "v0.0.3",
				projFS:      tc.projFS,
				packageFS:   afero.NewBasePathFs(tc.projFS, "_output"),
				transport:   transport,
			}

			ep, err := url.Parse("https://donotuse.example.com")
			assert.NilError(t, err)
			upCtx := &upbound.Context{
				Domain:           &url.URL{},
				RegistryEndpoint: ep,
			}
			err = c.Run(context.Background(), upCtx, &pterm.DefaultBasicText)
			assert.NilError(t, err)

			// Pull the configuration image from the server and unpack its
			// metadata.
			gotImg, err := remote.Image(repo.Tag("v0.0.3"), remote.WithTransport(transport))
			assert.NilError(t, err)

			m, err := xpkgmarshaler.NewMarshaler()
			assert.NilError(t, err)
			cfgPkg, err := m.FromImage(xpkg.Image{
				Image: gotImg,
			})
			assert.NilError(t, err)
			cfgMeta, ok := cfgPkg.Meta().(*xpmetav1.Configuration)
			assert.Assert(t, ok, "unexpected metadata type for configuration")

			// Make sure we can pull the embedded functions the configuration
			// depends on.
			assert.Assert(t, cmp.Len(cfgMeta.Spec.DependsOn, tc.expectedFunctionCount))
			for _, dep := range cfgMeta.Spec.DependsOn {
				depRepo, err := name.NewRepository(*dep.Function)
				assert.NilError(t, err)
				depRef := depRepo.Digest(dep.Version)

				gotDep, err := remote.Image(depRef, remote.WithTransport(transport))
				assert.NilError(t, err)
				cfgPkg, err := m.FromImage(xpkg.Image{
					Image: gotDep,
				})
				assert.NilError(t, err)
				_, ok := cfgPkg.Meta().(*xpmetav1.Function)
				assert.Assert(t, ok, "unexpected metadata type for function")
			}
		})
	}
}

type hostReplacementRoundTripper struct {
	wrap http.RoundTripper
	from string
	to   string
}

func (h *hostReplacementRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Host = strings.Replace(r.URL.Host, h.from, h.to, 1)
	return h.wrap.RoundTrip(r)
}
