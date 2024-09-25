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
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/util/rand"
)

const (
	errNoSuitableBuilder = "no suitable builder found"
)

// Identifier knows how to identify an appropriate builder for a function based
// on it source code.
type Identifier interface {
	// Identify returns a suitable builder for the function whose source lives
	// in the given filesystem. It returns an error if no such builder is
	// available.
	Identify(fromFS afero.Fs) (Builder, error)
}

type realIdentifier struct{}

// DefaultIdentifier is the default builder identifier, suitable for production
// use.
var DefaultIdentifier = realIdentifier{}

func (realIdentifier) Identify(fromFS afero.Fs) (Builder, error) {
	// builders are the known builder types, in order of precedence.
	builders := []Builder{
		&dockerBuilder{},
		&kclBuilder{},
	}
	for _, b := range builders {
		ok, err := b.match(fromFS)
		if err != nil {
			return nil, errors.Wrapf(err, "builder %q returned an error", b.Name())
		}
		if ok {
			return b, nil
		}
	}

	return nil, errors.New(errNoSuitableBuilder)
}

type nopIdentifier struct{}

// FakeIdentifier is an identifier that always returns a fake builder. This is
// for use in tests where we don't want to do real builds.
var FakeIdentifier = nopIdentifier{}

func (nopIdentifier) Identify(fromFS afero.Fs) (Builder, error) {
	return &fakeBuilder{}, nil
}

// Builder knows how to build a particular kind of function.
type Builder interface {
	// Name returns a name for this builder.
	Name() string
	// Build builds the function whose source lives in the given filesystem,
	// returning an image for each architecture. This image will *not* include
	// package metadata; it's just the runtime image for the function.
	Build(ctx context.Context, fromFS afero.Fs, architectures []string) ([]v1.Image, error)

	// match returns true if this builder can build the function whose source
	// lives in the given filesystem.
	match(fromFS afero.Fs) (bool, error)
}

// dockerBuilder builds functions from a Dockerfile. For now, it relies on a
// Docker daemon being available.
type dockerBuilder struct{}

func (b *dockerBuilder) Name() string {
	return "docker"
}

func (b *dockerBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "Dockerfile")
}

func (b *dockerBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string) ([]v1.Image, error) {
	cl, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to docker daemon")
	}

	// Collect build context to send to the docker daemon.
	contextTar, err := fsToTar(fromFS, "/")
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct docker context")
	}
	tag := fmt.Sprintf("up-build:%s", rand.String(12))

	images := make([]v1.Image, len(architectures))
	eg, ctx := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		// Pin loop vars
		i, arch := i, arch
		eg.Go(func() error {
			dockerContext := bytes.NewReader(contextTar)
			// We tag the image only so we can reliably get it back from the Docker
			// daemon. This tag never gets used outside of the build process.
			archTag := fmt.Sprintf("%s-%s", tag, arch)
			opts := types.ImageBuildOptions{
				Tags:           []string{archTag},
				Platform:       "linux/" + arch,
				SuppressOutput: true,
			}
			_, err := cl.ImageBuild(ctx, dockerContext, opts)
			if err != nil {
				return errors.Wrap(err, "failed to build image")
			}

			ref, err := name.NewTag(archTag)
			if err != nil {
				return errors.Wrap(err, "failed to parse image digest from build response")
			}

			img, err := daemon.Image(ref, daemon.WithContext(ctx))
			if err != nil {
				return errors.Wrap(err, "failed to fetch built image from docker daemon")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

// kclBuilder builds functions written in KCL by injecting their code into a
// function-kcl base image.
type kclBuilder struct{}

func (b *kclBuilder) Name() string {
	return "kcl"
}

func (b *kclBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "kcl.mod")
}

func (b *kclBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string) ([]v1.Image, error) {
	baseRef, err := name.NewTag("xpkg.upbound.io/awg/function-kcl-base:latest")
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse KCL base image tag")
	}

	images := make([]v1.Image, len(architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		// Pin loop vars
		i, arch := i, arch
		eg.Go(func() error {
			// NOTE: Don't pass remote.WithContext(), since fetching of remote
			// layers uses the given context and that doesn't happen until well
			// after this function returns.
			baseImg, err := remote.Image(baseRef, remote.WithPlatform(v1.Platform{
				OS:           "linux",
				Architecture: arch,
			}))
			if err != nil {
				return errors.Wrap(err, "failed to pull KCL base image")
			}

			cfg, err := baseImg.ConfigFile()
			if err != nil {
				return errors.Wrap(err, "failed to get config from KCL base image")
			}
			if cfg.Architecture != arch {
				return errors.Errorf("KCL base image not available for architecture %q", arch)
			}

			src, err := fsToTar(fromFS, "/src")
			if err != nil {
				return errors.Wrap(err, "failed to tar layer contents")
			}

			codeLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(src)), nil
			})
			if err != nil {
				return errors.Wrap(err, "failed to create code layer")
			}

			img, err := mutate.AppendLayers(baseImg, codeLayer)
			if err != nil {
				return errors.Wrap(err, "failed to add code to image")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

// fakeBuilder builds empty images with correct configs. It is intended for use
// in unit tests. It matches any input.
type fakeBuilder struct{}

func (b *fakeBuilder) Name() string {
	return "fake"
}

func (b *fakeBuilder) match(fromFS afero.Fs) (bool, error) {
	return true, nil
}

func (b *fakeBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string) ([]v1.Image, error) {
	images := make([]v1.Image, len(architectures))
	for i, arch := range architectures {
		baseImg := empty.Image
		cfg := &v1.ConfigFile{
			OS:           "linux",
			Architecture: arch,
		}
		img, err := mutate.ConfigFile(baseImg, cfg)
		if err != nil {
			return nil, err
		}
		images[i] = img
	}

	return images, nil
}

func fsToTar(f afero.Fs, prefix string) ([]byte, error) {
	// Copied from tar.AddFS but prepend the prefix.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := tw.WriteHeader(&tar.Header{
		Name:     prefix,
		Typeflag: tar.TypeDir,
		Mode:     0777,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create prefix directory in tar archive")
	}
	err = afero.Walk(f, ".", func(name string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// TODO(#49580): Handle symlinks when fs.ReadLinkFS is available.
		if !info.Mode().IsRegular() {
			return errors.New("tar: cannot add non-regular file")
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.Join(prefix, name)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := f.Open(name)
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck // Copied from upstream.
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to populate tar archive")
	}
	err = tw.Close()
	if err != nil {
		return nil, errors.Wrap(err, "failed to close tar archive")
	}

	return buf.Bytes(), nil
}
