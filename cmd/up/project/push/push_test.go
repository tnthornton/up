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
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"gotest.tools/v3/assert"

	"github.com/upbound/up/internal/upbound"
)

//go:embed testdata/demo-project/**
var demoProject embed.FS

func TestPush(t *testing.T) {
	const (
		pkgFileName = "demo-project-v0.0.3.uppkg"
		pkgTag      = "v0.0.3"
	)

	// Start a test registry so we can actually do a push.
	reg, err := registry.TLS("localhost")
	assert.NilError(t, err)
	t.Cleanup(reg.Close)

	projFS := afero.NewBasePathFs(afero.FromIOFS{FS: demoProject}, "testdata/demo-project")
	pkgFS := afero.NewMemMapFs()

	// Replace the tag in the image to include the correct address of the test
	// registry so that the push command can find it in the tarball.
	repo := strings.TrimPrefix(reg.URL, "https://") + "/demo/project"
	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", repo, pkgTag))
	assert.NilError(t, err)
	img, err := tarball.Image(func() (io.ReadCloser, error) {
		return projFS.Open(filepath.Join("_output", pkgFileName))
	}, nil)
	assert.NilError(t, err)
	imgWriter, err := pkgFS.Create(pkgFileName)
	assert.NilError(t, err)
	err = tarball.Write(imgTag, img, imgWriter)
	assert.NilError(t, err)
	err = imgWriter.Close()
	assert.NilError(t, err)

	c := &Cmd{
		ProjectFile: "upbound.yaml",
		Repository:  repo,
		Tag:         "v0.0.3",
		projFS:      projFS,
		packageFS:   pkgFS,
		transport:   reg.Client().Transport,
	}

	upCtx := &upbound.Context{
		Domain: &url.URL{},
	}
	err = c.Run(context.Background(), upCtx, &pterm.DefaultBasicText)
	assert.NilError(t, err)

	// Check that the server actually accepted the image. This isn't a super
	// check, but good enough for now.
	_, err = remote.Head(imgTag, remote.WithTransport(reg.Client().Transport))
	assert.NilError(t, err)
}
