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
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/yaml"

	"github.com/upbound/up-sdk-go/service/repositories"
	"github.com/upbound/up/cmd/up/project/build"
	"github.com/upbound/up/internal/credhelper"
	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/upterm"
	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

type Cmd struct {
	ProjectFile    string        `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Repository     string        `optional:"" help:"Repository to push to. Overrides the repository specified in the project file."`
	Tag            string        `short:"t" help:"Tag for the built package. If not provided, a semver tag will be generated." default:""`
	PackageFile    string        `optional:"" help:"Package file to push. Discovered by default based on repository and tag."`
	Create         bool          `help:"Create the configuration repository before pushing if it does not exist. Function sub-repositories will always be created automatically."`
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

	if c.Tag == "" {
		// TODO(adamwg): Consider smarter tag generation using git metadata if
		// the project lives in a git repository, or the package digest.
		c.Tag = fmt.Sprintf("v0.0.0-%d", time.Now().Unix())
	}

	c.transport = http.DefaultTransport

	return nil
}

func (c *Cmd) Run(ctx context.Context, upCtx *upbound.Context, p pterm.TextPrinter) error { //nolint:gocyclo // This isn't too complex.
	pterm.EnableStyling()

	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 1
	}

	project, err := c.parseProject()
	if err != nil {
		return err
	}

	imgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, c.Tag))
	if err != nil {
		return errors.Wrap(err, "failed to construct image tag")
	}

	if c.Create {
		if !isUpboundRepository(upCtx, imgTag.Repository) {
			return errors.New("cannot create repository for non-Upbound registry")
		}
		err = c.createRepository(ctx, upCtx, imgTag.Repository)
		if err != nil {
			return err
		}
	}

	if c.PackageFile == "" {
		c.PackageFile = fmt.Sprintf("%s.uppkg", project.Name)
	}

	// Collect the images from the on-disk package and sort them into
	// repositories.
	var (
		cfgImage v1.Image
		fnImages map[name.Repository][]v1.Image
	)
	cfgTag, err := name.NewTag(fmt.Sprintf("%s:%s", project.Spec.Repository, build.ConfigurationTag))
	if err != nil {
		return errors.Wrap(err, "failed to construct configuration tag")
	}
	err = upterm.WrapWithSuccessSpinner(fmt.Sprintf("Loading packages from %s", c.PackageFile), upterm.CheckmarkSuccessSpinner, func() error {
		cfgImage, fnImages, err = collectImages(c.packageFS, c.PackageFile, cfgTag)
		return err
	})
	if err != nil {
		return err
	}

	// Push all the function packages in parallel.
	eg, egCtx := errgroup.WithContext(ctx)
	// Semaphore to limit the number of functions we push in parallel.
	sem := make(chan struct{}, c.MaxConcurrency)
	for repo, images := range fnImages {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			// Create the subrepository if needed. We can only do this for the
			// Upbound registry; assume other registries will create on push.
			if isUpboundRepository(upCtx, repo) {
				err := c.createRepository(egCtx, upCtx, repo)
				if err != nil {
					return errors.Wrapf(err, "failed to create repository for function %q", repo)
				}
			}

			err = c.pushIndex(egCtx, upCtx, repo, images...)
			if err != nil {
				return errors.Wrapf(err, "failed to push function %q", repo)
			}
			return nil
		})
	}

	err = upterm.WrapWithSuccessSpinner("Pushing functions", upterm.CheckmarkSuccessSpinner, eg.Wait)
	if err != nil {
		return err
	}

	// Once the functions are pushed, push the configuration package.
	err = upterm.WrapWithSuccessSpinner(fmt.Sprintf("Pushing %s", imgTag), upterm.CheckmarkSuccessSpinner, func() error {
		return c.pushImage(ctx, upCtx, imgTag, cfgImage)
	})
	if err != nil {
		return errors.Wrap(err, "failed to push configuration package")
	}
	return nil
}

func collectImages(pkgFS afero.Fs, fname string, cfgTag name.Tag) (v1.Image, map[name.Repository][]v1.Image, error) {
	opener := func() (io.ReadCloser, error) {
		return pkgFS.Open(fname)
	}
	mfst, err := tarball.LoadManifest(opener)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to read package")
	}

	var (
		fnImages = make(map[name.Repository][]v1.Image)
		cfgImage v1.Image
	)
	for _, desc := range mfst {
		if slices.Contains(desc.RepoTags, cfgTag.String()) {
			cfgImage, err = tarball.Image(opener, &cfgTag)
			if err != nil {
				return nil, nil, errors.Wrap(err, "failed to load configuration image from package")
			}
			cfgImage, err = xpkg.AnnotateImage(cfgImage)
			if err != nil {
				return nil, nil, errors.Wrap(err, "failed to annotate configuration image")
			}
		} else {
			fnTag, err := name.NewTag(desc.RepoTags[0])
			if err != nil {
				return nil, nil, errors.Wrapf(err, "failed to parse function image tag %q", desc.RepoTags[0])
			}
			fnImage, err := tarball.Image(opener, &fnTag)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "failed to load function %q image from package", fnTag)
			}
			fnImages[fnTag.Repository] = append(fnImages[fnTag.Repository], fnImage)
		}
	}

	if cfgImage == nil {
		return nil, nil, errors.New("failed to find configuration image in package")
	}

	return cfgImage, fnImages, nil
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

func (c *Cmd) createRepository(ctx context.Context, upCtx *upbound.Context, repo name.Repository) error {
	account, repoName, ok := strings.Cut(repo.RepositoryStr(), "/")
	if !ok {
		return errors.New("invalid repository: must be of the form <account>/<name>")
	}
	cfg, err := upCtx.BuildSDKConfig()
	if err != nil {
		return err
	}
	// TODO(adamwg): Make the repository private by default.
	if err := repositories.NewClient(cfg).CreateOrUpdate(ctx, account, repoName); err != nil {
		return errors.Wrap(err, "failed to create repository")
	}

	return nil
}

func isUpboundRepository(upCtx *upbound.Context, tag name.Repository) bool {
	return strings.HasPrefix(tag.RegistryStr(), upCtx.RegistryEndpoint.Hostname())
}

func (c *Cmd) pushIndex(ctx context.Context, upCtx *upbound.Context, repo name.Repository, imgs ...v1.Image) error {
	kc := authn.NewMultiKeychain(
		authn.NewKeychainFromHelper(
			credhelper.New(
				credhelper.WithDomain(upCtx.Domain.Hostname()),
				credhelper.WithProfile(c.Flags.Profile),
			),
		),
		authn.DefaultKeychain,
	)

	// Build an index. This is a little superfluous if there's only one image
	// (single architecture), but we generate configuration dependencies on
	// embedded functions assuming there's an index, so we push an index
	// regardless of whether we really need one.
	idx, imgs, err := xpkg.BuildIndex(imgs...)
	if err != nil {
		return err
	}

	// Push the images by digest.
	for _, img := range imgs {
		dgst, err := img.Digest()
		if err != nil {
			return err
		}
		err = c.pushImage(ctx, upCtx, repo.Digest(dgst.String()), img)
		if err != nil {
			return err
		}
	}

	// Tag the function the same as the configuration. The configuration depends
	// on it by digest, so this isn't necessary for things to work correctly,
	// but it makes the Marketplace experience more intuitive for the user.
	tag := repo.Tag(c.Tag)
	return remote.WriteIndex(tag, idx,
		remote.WithAuthFromKeychain(kc),
		remote.WithContext(ctx),
		remote.WithTransport(c.transport),
	)
}

func (c *Cmd) pushImage(ctx context.Context, upCtx *upbound.Context, ref name.Reference, img v1.Image) error {
	kc := authn.NewMultiKeychain(
		authn.NewKeychainFromHelper(
			credhelper.New(
				credhelper.WithDomain(upCtx.Domain.Hostname()),
				credhelper.WithProfile(c.Flags.Profile),
			),
		),
		authn.DefaultKeychain,
	)

	return remote.Write(ref, img,
		remote.WithAuthFromKeychain(kc),
		remote.WithContext(ctx),
		remote.WithTransport(c.transport),
	)
}
