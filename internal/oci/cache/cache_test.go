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

package cache

import (
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"gotest.tools/v3/assert"
)

func TestPut(t *testing.T) {
	t.Parallel()

	wrap := newFakeCache()
	cch := NewValidatingCache(wrap)
	expectedLayer := randomLayer(t)
	expectedHash, err := expectedLayer.Digest()
	assert.NilError(t, err)

	gotLayer, err := cch.Put(expectedLayer)
	assert.NilError(t, err)

	gotHash, err := gotLayer.Digest()
	assert.NilError(t, err)
	assert.Equal(t, gotHash, expectedHash)

	wrapLayer, err := wrap.Get(expectedHash)
	assert.NilError(t, err)
	wrapHash, err := wrapLayer.Digest()
	assert.NilError(t, err)
	assert.Equal(t, wrapHash, expectedHash)
}

func TestGet(t *testing.T) {
	t.Parallel()

	// Create some fixture layers for use below.
	layer1 := randomLayer(t)
	hash1, err := layer1.Digest()
	assert.NilError(t, err)
	layer2 := randomLayer(t)

	tcs := map[string]struct {
		setupWrap     func() *fakeCache
		hash          v1.Hash
		expectedHash  v1.Hash
		expectedError error
	}{
		"NotFound": {
			setupWrap:     newFakeCache,
			hash:          hash1,
			expectedError: cache.ErrNotFound,
		},
		"Found": {
			setupWrap: func() *fakeCache {
				wrap := newFakeCache()
				wrap.layers[hash1] = layer1
				return wrap
			},
			hash:          hash1,
			expectedError: nil,
			expectedHash:  hash1,
		},
		"Corrupt": {
			setupWrap: func() *fakeCache {
				wrap := newFakeCache()
				// Cache is corrupt - hash1 points at layer2.
				wrap.layers[hash1] = layer2
				return wrap
			},
			hash:          hash1,
			expectedError: cache.ErrNotFound,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			wrap := tc.setupWrap()
			cch := NewValidatingCache(wrap)

			gotLayer, err := cch.Get(tc.hash)
			assert.ErrorIs(t, err, tc.expectedError)
			if tc.expectedError == nil {
				gotHash, err := gotLayer.Digest()
				assert.NilError(t, err)
				assert.Equal(t, gotHash, tc.expectedHash)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()

	layer := randomLayer(t)
	hash, err := layer.Digest()
	assert.NilError(t, err)
	wrap := newFakeCache()
	wrap.layers[hash] = layer
	cch := NewValidatingCache(wrap)

	err = cch.Delete(hash)
	assert.NilError(t, err)

	_, ok := wrap.layers[hash]
	assert.Assert(t, !ok, "layer still in cache after delete")
}

type fakeCache struct {
	layers map[v1.Hash]v1.Layer
}

func (c *fakeCache) Put(l v1.Layer) (v1.Layer, error) {
	h, _ := l.Digest()
	c.layers[h] = l
	return l, nil
}

func (c *fakeCache) Get(h v1.Hash) (v1.Layer, error) {
	l, ok := c.layers[h]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return l, nil
}

func (c *fakeCache) Delete(h v1.Hash) error {
	delete(c.layers, h)
	return nil
}

func newFakeCache() *fakeCache {
	return &fakeCache{
		layers: make(map[v1.Hash]v1.Layer),
	}
}

func randomLayer(t *testing.T) v1.Layer {
	l, err := random.Layer(1024, types.OCILayer)
	assert.NilError(t, err)
	return l
}
