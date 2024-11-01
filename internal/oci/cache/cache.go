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
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/cache"
)

// validatingCache is a v1.Cache that checks layer digests when reading
// layers from an underlying cache. This ensures that if the underlying cache is
// corrupt we don't end up with corrupt layers.
type validatingCache struct {
	wrap cache.Cache
}

func (c *validatingCache) Put(l v1.Layer) (v1.Layer, error) {
	return c.wrap.Put(l)
}

func (c *validatingCache) Get(h v1.Hash) (v1.Layer, error) {
	l, err := c.wrap.Get(h)
	if err != nil {
		return l, err
	}

	// Check the digest of the layer returned from the underlying cache. If we
	// can't calculate the digest, or it doesn't match the hash the caller asked
	// for, remove it from the cache and return not found so the cache will be
	// repopulated with a correct layer.
	dgst, err := l.Digest()
	if err != nil || dgst != h {
		_ = c.wrap.Delete(h)
		return l, cache.ErrNotFound
	}

	return l, nil
}

func (c *validatingCache) Delete(h v1.Hash) error {
	return c.wrap.Delete(h)
}

func NewValidatingCache(wrap cache.Cache) *validatingCache {
	return &validatingCache{
		wrap: wrap,
	}
}
