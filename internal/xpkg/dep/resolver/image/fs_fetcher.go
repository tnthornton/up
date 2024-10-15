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

package image

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
)

// FSFetcher is an image fetcher that returns packages stored in a
// filesystem. The directory structure should be
// <registry>/<repository>/<tag>.xpkg. Note that <repository> may contain an
// arbitrary number of slashes (i.e., should have nested directories). This is
// intended to be used in unit tests with go:embed.
type FSFetcher struct {
	FS afero.Fs
}

// Fetch returns the configured error.
func (m *FSFetcher) Fetch(ctx context.Context, ref name.Reference, secrets ...string) (v1.Image, error) {
	fname := filepath.Join(ref.Context().String(), ref.Identifier()) + ".xpkg"

	return tarball.Image(func() (io.ReadCloser, error) {
		return m.FS.Open(fname)
	}, nil)
}

// Head returns the configured error.
func (m *FSFetcher) Head(ctx context.Context, ref name.Reference, secrets ...string) (*v1.Descriptor, error) {
	img, err := m.Fetch(ctx, ref, secrets...)
	if err != nil {
		return nil, err
	}
	mt, err := img.MediaType()
	if err != nil {
		return nil, err
	}
	dgst, err := img.Digest()
	if err != nil {
		return nil, err
	}
	return &v1.Descriptor{
		MediaType: mt,
		Digest:    dgst,
	}, nil
}

// Tags returns the configured tags or if none exist then error.
func (m *FSFetcher) Tags(ctx context.Context, ref name.Reference, secrets ...string) ([]string, error) {
	repoName := ref.Context().String()
	infos, err := afero.ReadDir(m.FS, repoName)
	if err != nil {
		return nil, err
	}

	tags := make([]string, len(infos))
	for i, info := range infos {
		ext := filepath.Ext(info.Name())
		tags[i] = strings.TrimSuffix(info.Name(), ext)
	}

	return tags, nil
}
