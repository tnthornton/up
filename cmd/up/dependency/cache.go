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

package dependency

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"

	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"
)

const (
	errMetaFileNotFound = "metadata file (crossplane.yaml or upbound.yaml) not found in current directory"
)

// updateCacheCmd updates the cache.
type updateCacheCmd struct {
	c        *cache.Local
	m        *manager.Manager
	ws       *workspace.Workspace
	modelsFS afero.Fs

	ProjectFile string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	// TODO(@tnthornton) remove cacheDir flag. Having a user supplied flag
	// can result in broken behavior between xpls and dep. CacheDir should
	// only be supplied by the Config.
	CacheDir string `short:"d" help:"Directory used for caching package images." default:"~/.up/cache/" env:"CACHE_DIR" type:"path"`
}

func (c *updateCacheCmd) AfterApply(kongCtx *kong.Context, p pterm.TextPrinter) error {
	kongCtx.Bind(pterm.DefaultBulletList.WithWriter(kongCtx.Stdout))
	ctx := context.Background()

	// Read the project file.
	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	// The location of the project file defines the root of the project.
	projDirPath := filepath.Dir(projFilePath)
	c.modelsFS = afero.NewBasePathFs(afero.NewOsFs(), filepath.Join(projDirPath, ".up"))

	fs := afero.NewOsFs()

	cache, err := cache.NewLocal(c.CacheDir, cache.WithFS(fs))
	if err != nil {
		return err
	}

	c.c = cache

	r := image.NewResolver()

	m, err := manager.New(
		manager.WithCacheModels(c.modelsFS),
		manager.WithCache(cache),
		manager.WithResolver(r),
	)

	if err != nil {
		return err
	}

	c.m = m

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	ws, err := workspace.New(wd,
		workspace.WithFS(fs),
		// The user doesn't care about workspace warnings during up dep update-cache.
		workspace.WithPrinter(&pterm.BasicTextPrinter{Writer: io.Discard}),
		workspace.WithPermissiveParser(),
	)
	if err != nil {
		return err
	}
	c.ws = ws

	if err := ws.Parse(ctx); err != nil {
		return err
	}

	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

func (c *updateCacheCmd) Run(ctx context.Context, p pterm.TextPrinter, pb *pterm.BulletListPrinter) error {
	meta := c.ws.View().Meta()
	if meta == nil {
		return errors.New(errMetaFileNotFound)
	}

	metaDeps, err := meta.DependsOn()
	if err != nil {
		return err
	}

	p.Printfln("Updating %d dependencies...", len(metaDeps))

	resolvedDeps := make([]v1beta1.Dependency, len(metaDeps))
	for i, d := range metaDeps {
		ud, _, err := c.m.AddAll(ctx, d)
		if err != nil {
			return err
		}
		resolvedDeps[i] = ud
	}

	if len(resolvedDeps) == 0 {
		p.Printfln("No dependencies specified")
		return nil
	}
	p.Printfln("Dependencies added to cache:")
	li := make([]pterm.BulletListItem, len(resolvedDeps))
	for i, d := range resolvedDeps {
		li[i] = pterm.BulletListItem{
			Level:  0,
			Text:   fmt.Sprintf("%s (%s)", d.Package, d.Constraints),
			Bullet: "-",
		}
	}
	// TODO(hasheddan): bullet list printer incorrectly appends an extra
	// trailing newline. Update when fixed upstream.
	return pb.WithItems(li).Render()
}

// cleanCacheCmd updates the cache.
type cleanCacheCmd struct {
	c *cache.Local

	// TODO(@tnthornton) remove cacheDir flag. Having a user supplied flag
	// can result in broken behavior between xpls and dep. CacheDir should
	// only be supplied by the Config.
	CacheDir string `short:"d" help:"Directory used for caching package images." default:"~/.up/cache/" env:"CACHE_DIR" type:"path"`
}

func (c *cleanCacheCmd) AfterApply(kongCtx *kong.Context, p pterm.TextPrinter) error {
	kongCtx.Bind(pterm.DefaultBulletList.WithWriter(kongCtx.Stdout))
	ctx := context.Background()
	fs := afero.NewOsFs()

	cache, err := cache.NewLocal(c.CacheDir, cache.WithFS(fs))
	if err != nil {
		return err
	}

	c.c = cache

	// workaround interfaces not being bindable ref: https://github.com/alecthomas/kong/issues/48
	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

func (c *cleanCacheCmd) Run(ctx context.Context, p pterm.TextPrinter) error {
	if err := c.c.Clean(); err != nil {
		return err
	}
	p.Printfln("xpkg cache cleaned")
	return nil
}
