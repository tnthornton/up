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
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"

	"github.com/upbound/up/internal/async"
	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/upterm"
)

type Cmd struct {
	ProjectFile    string        `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Repository     string        `optional:"" help:"Repository to push to. Overrides the repository specified in the project file."`
	Tag            string        `short:"t" help:"Tag for the built package. If not provided, a semver tag will be generated." default:""`
	PackageFile    string        `optional:"" help:"Package file to push. Discovered by default based on repository and tag."`
	MaxConcurrency uint          `help:"Maximum number of functions to build at once." env:"UP_MAX_CONCURRENCY" default:"8"`
	Flags          upbound.Flags `embed:""`

	projFS    afero.Fs
	packageFS afero.Fs
	transport http.RoundTripper
}

func (c *Cmd) AfterApply(kongCtx *kong.Context) error {
	upCtx, err := upbound.NewFromFlags(c.Flags)
	if err != nil {
		return err
	}
	upCtx.SetupLogging()
	kongCtx.Bind(upCtx)

	// Read the project file.
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	// The location of the project file defines the root of the project.
	projDirPath := filepath.Dir(projFilePath)
	// Construct a virtual filesystem that contains only the project. We'll do
	// all our operations inside this virtual FS.
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	// If the package file was provided, construct an output FS. Default to the
	// `_output` dir of the project, since that's where `up project build` puts
	// packages.
	if c.PackageFile == "" {
		c.packageFS = afero.NewBasePathFs(c.projFS, "/_output")
	} else {
		c.packageFS = afero.NewOsFs()
	}

	c.transport = http.DefaultTransport

	return nil
}

func (c *Cmd) Run(ctx context.Context, upCtx *upbound.Context, p pterm.TextPrinter) error {
	pterm.EnableStyling()

	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 1
	}

	projFilePath := filepath.Join("/", filepath.Base(c.ProjectFile))
	proj, err := project.Parse(c.projFS, projFilePath)
	if err != nil {
		return err
	}

	if c.Repository != "" {
		proj.Spec.Repository = c.Repository
	}
	if c.PackageFile == "" {
		c.PackageFile = fmt.Sprintf("%s.uppkg", proj.Name)
	}

	// Load the packages from disk.
	var imgMap project.ImageTagMap
	err = upterm.WrapWithSuccessSpinner(
		fmt.Sprintf("Loading packages from %s", c.PackageFile),
		upterm.CheckmarkSuccessSpinner,
		func() error {
			imgMap, err = c.loadPackages()
			return err
		},
	)

	pusher := project.NewPusher(
		project.PushWithUpboundContext(upCtx),
		project.PushWithTransport(c.transport),
		project.PushWithMaxConcurrency(c.MaxConcurrency),
	)

	err = async.WrapWithSuccessSpinners(func(ch async.EventChannel) error {
		opts := []project.PushOption{
			project.PushWithEventChannel(ch),
		}
		if c.Tag != "" {
			opts = append(opts, project.PushWithTag(c.Tag))
		}

		_, err := pusher.Push(ctx, proj, imgMap, opts...)
		return err
	})
	if err != nil {
		return err
	}

	return nil
}

func (c *Cmd) loadPackages() (project.ImageTagMap, error) {
	opener := func() (io.ReadCloser, error) {
		return c.packageFS.Open(c.PackageFile)
	}
	mfst, err := tarball.LoadManifest(opener)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read package file manifest")
	}

	imgMap := make(project.ImageTagMap)
	for _, desc := range mfst {
		if len(desc.RepoTags) == 0 {
			// Ignore images with no tags; we shouldn't find these in uppkg
			// files, but best not to panic if it happens.
			continue
		}

		tag, err := name.NewTag(desc.RepoTags[0])
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse image tag %q", desc.RepoTags[0])
		}
		image, err := tarball.Image(opener, &tag)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to load image %q from package", tag)
		}
		imgMap[tag] = image
	}

	return imgMap, nil
}
