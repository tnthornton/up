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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"

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

	"github.com/upbound/up/internal/filesystem"
	"github.com/upbound/up/internal/xpkg"
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
		newKCLBuilder(),
		newPythonBuilder(),
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
	Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath *string) ([]v1.Image, error)

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

func (b *dockerBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath *string) ([]v1.Image, error) {
	cl, err := client.NewClientWithOpts(client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to docker daemon")
	}

	// Collect build context to send to the docker daemon.
	contextTar, err := filesystem.FSToTar(fromFS, "/", nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to construct docker context")
	}
	tag := fmt.Sprintf("up-build:%s", rand.String(12))

	images := make([]v1.Image, len(architectures))
	eg, ctx := errgroup.WithContext(ctx)
	for i, arch := range architectures {
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

			img, err := daemon.Image(ref)
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
type kclBuilder struct {
	baseImage string
	transport http.RoundTripper
}

func (b *kclBuilder) Name() string {
	return "kcl"
}

func (b *kclBuilder) match(fromFS afero.Fs) (bool, error) {
	return afero.Exists(fromFS, "kcl.mod")
}

func (b *kclBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath *string) ([]v1.Image, error) {
	baseRef, err := name.NewTag(b.baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse KCL base image tag")
	}

	images := make([]v1.Image, len(architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		eg.Go(func() error {
			baseImg, err := baseImageForArch(baseRef, arch, b.transport)
			if err != nil {
				return errors.Wrap(err, "failed to fetch KCL base image")
			}

			src, err := filesystem.FSToTar(fromFS, "/src", osBasePath)
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

			// Set the default source to match our source directory.
			img, err = setImageEnvvar(img, "FUNCTION_KCL_DEFAULT_SOURCE", "/src")
			if err != nil {
				return errors.Wrap(err, "failed to configure KCL source path")
			}

			images[i] = img
			return nil
		})
	}

	return images, eg.Wait()
}

// pythonBuilder builds functions written in python by injecting their code into a
// function-python base image.
type pythonBuilder struct {
	baseImage   string
	packagePath string
	transport   http.RoundTripper
}

func (b *pythonBuilder) Name() string {
	return "python"
}

func (b *pythonBuilder) match(fromFS afero.Fs) (bool, error) {
	// More reliable than requirements.txt, which is optional.
	return afero.Exists(fromFS, "main.py")
}

func (b *pythonBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string) ([]v1.Image, error) {
	baseRef, err := name.NewTag(b.baseImage)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse python base image tag")
	}

	images := make([]v1.Image, len(architectures))
	eg, _ := errgroup.WithContext(ctx)
	for i, arch := range architectures {
		eg.Go(func() error {
			baseImg, err := remote.Image(baseRef, remote.WithPlatform(v1.Platform{
				OS:           "linux",
				Architecture: arch,
			}), remote.WithTransport(b.transport))
			if err != nil {
				return errors.Wrap(err, "failed to fetch python base image")
			}

			src, err := filesystem.FSToTar(fromFS, b.packagePath)
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

// baseImageForArch pulls the image with the given ref, and returns a version of
// it suitable for use as a function base image. Specifically, the package
// layer, examples layer, and schema layers will be removed if present. Note
// that layers in the returned image will refer to the remote and be pulled only
// if they are read by the caller.
func baseImageForArch(ref name.Reference, arch string, transport http.RoundTripper) (v1.Image, error) {
	img, err := remote.Image(ref, remote.WithPlatform(v1.Platform{
		OS:           "linux",
		Architecture: arch,
	}), remote.WithTransport(transport))
	if err != nil {
		return nil, errors.Wrap(err, "failed to pull image")
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config from image")
	}
	if cfg.Architecture != arch {
		return nil, errors.Errorf("image not available for architecture %q", arch)
	}

	// Remove the package layer and schema layers if present.
	mfst, err := img.Manifest()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get manifest from image")
	}
	baseImage := empty.Image
	// The RootFS contains a list of layers; since we're removing layers we need
	// to clear it out. It will be rebuilt by the mutate package.
	cfg.RootFS = v1.RootFS{}
	baseImage, err = mutate.ConfigFile(baseImage, cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to add configuration to base image")
	}
	for _, desc := range mfst.Layers {
		if isNonBaseLayer(desc) {
			continue
		}
		l, err := img.LayerByDigest(desc.Digest)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get layer from image")
		}
		baseImage, err = mutate.AppendLayers(baseImage, l)
		if err != nil {
			return nil, errors.Wrap(err, "failed to add layer to base image")
		}
	}

	return baseImage, nil
}

func isNonBaseLayer(desc v1.Descriptor) bool {
	nonBaseLayerAnns := []string{
		xpkg.PackageAnnotation,
		xpkg.ExamplesAnnotation,
		xpkg.SchemaKclAnnotation,
		xpkg.SchemaPythonAnnotation,
	}

	ann := desc.Annotations[xpkg.AnnotationKey]
	return slices.Contains(nonBaseLayerAnns, ann)
}

func setImageEnvvar(image v1.Image, key string, value string) (v1.Image, error) {
	cfgFile, err := image.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config file")
	}
	cfg := cfgFile.Config
	cfg.Env = append(cfg.Env, fmt.Sprintf("%s=%s", key, value))

	image, err = mutate.Config(image, cfg)
	if err != nil {
		return nil, errors.Wrap(err, "failed to set config")
	}

	return image, nil
}

func newKCLBuilder() *kclBuilder {
	return &kclBuilder{
		baseImage: "xpkg.upbound.io/awg/function-kcl-base:v0.10.6-1-gf9733e3",
		transport: http.DefaultTransport,
	}
}

func newPythonBuilder() *pythonBuilder {
	return &pythonBuilder{
		// TODO(negz): Should this be hardcoded?
		baseImage: "xpkg.upbound.io/upbound/function-interpreter-python:v0.1.0",

		// TODO(negz): This'll need to change if function-interpreter-python is
		// updated to a distroless base layer that uses a newer Python version.
		packagePath: "/venv/fn/lib/python3.11/site-packages/function",
		transport:   http.DefaultTransport,
	}
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

func (b *fakeBuilder) Build(ctx context.Context, fromFS afero.Fs, architectures []string, osBasePath *string) ([]v1.Image, error) {
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
