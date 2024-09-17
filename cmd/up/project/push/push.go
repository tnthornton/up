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
	"strings"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up-sdk-go/service/repositories"
	"github.com/upbound/up/internal/credhelper"
	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/upterm"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

type Cmd struct {
	ProjectFile string        `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Repository  string        `optional:"" help:"Repository to push to. Overrides the repository specified in the project file."`
	Tag         string        `short:"t" help:"Tag for the built package." default:"latest"`
	PackageFile string        `optional:"" help:"Package file to push. Discovered by default based on repository and tag."`
	Create      bool          `help:"Create the repository before pushing if it does not exist."`
	Flags       upbound.Flags `embed:""`

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
	project, err := c.parseProject()
	if err != nil {
		return err
	}

	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, c.Tag))
	if err != nil {
		return errors.Wrap(err, "failed to construct image tag")
	}

	if c.Create {
		err = c.createRepository(ctx, upCtx, imgTag)
		if err != nil {
			return err
		}
	}

	if c.PackageFile == "" {
		pkgName := fmt.Sprintf("%s-%s.xpkg", project.Name, c.Tag)
		c.PackageFile = xpkg.BuildPath("", pkgName)
	}

	img, err := tarball.Image(func() (io.ReadCloser, error) {
		return c.packageFS.Open(c.PackageFile)
	}, &imgTag)
	if err != nil {
		return errors.Wrap(err, "failed to read package")
	}

	img, err = xpkg.AnnotateImage(img)
	if err != nil {
		return err
	}

	return c.pushImage(ctx, upCtx, imgTag, img)
}

func (c *Cmd) parseProject() (*v1alpha1.Project, error) {
	// Parse and validate the project file.
	projYAML, err := afero.ReadFile(c.projFS, filepath.Join("/", filepath.Base(c.ProjectFile)))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read project file %q", c.ProjectFile)
	}
	var project v1alpha1.Project
	err = yaml.Unmarshal(projYAML, &project)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse project file")
	}
	if err := project.Validate(); err != nil {
		return nil, errors.Wrap(err, "invalid project file")
	}

	if c.Repository != "" {
		project.Spec.Repository = c.Repository
	}

	return &project, nil
}

func (c *Cmd) createRepository(ctx context.Context, upCtx *upbound.Context, tag name.Tag) error {
	if !strings.Contains(tag.RegistryStr(), upCtx.RegistryEndpoint.Hostname()) {
		return errors.New("cannot create repository for non-Upbound registry")
	}
	account, repo, ok := strings.Cut(tag.RepositoryStr(), "/")
	if !ok {
		return errors.New("invalid repository: must be of the form <account>/<name>")
	}
	cfg, err := upCtx.BuildSDKConfig()
	if err != nil {
		return err
	}
	// TODO(adamwg): Make the repository private by default.
	if err := repositories.NewClient(cfg).CreateOrUpdate(ctx, account, repo); err != nil {
		return errors.Wrap(err, "failed to create repository")
	}

	return nil
}

func (c *Cmd) pushImage(ctx context.Context, upCtx *upbound.Context, tag name.Tag, img v1.Image) error {
	kc := authn.NewMultiKeychain(
		authn.NewKeychainFromHelper(
			credhelper.New(
				credhelper.WithDomain(upCtx.Domain.Hostname()),
				credhelper.WithProfile(c.Flags.Profile),
			),
		),
		authn.DefaultKeychain,
	)

	return upterm.WrapWithSuccessSpinner(fmt.Sprintf("Pushing %s", tag), upterm.CheckmarkSuccessSpinner, func() error {
		return remote.Write(tag, img,
			remote.WithAuthFromKeychain(kc),
			remote.WithContext(ctx),
			remote.WithTransport(c.transport),
		)
	})
}
