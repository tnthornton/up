// Copyright 2021 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dependency

import (
	"context"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"

	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/dep"
	"github.com/upbound/up/internal/xpkg/dep/cache"
	"github.com/upbound/up/internal/xpkg/dep/manager"
	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
	"github.com/upbound/up/internal/xpkg/workspace"
)

// addCmd manages crossplane dependencies.
type addCmd struct {
	m        *manager.Manager
	ws       *workspace.Workspace
	modelsFS afero.Fs

	Package     string `arg:"" help:"Package to be added."`
	ProjectFile string `short:"f" help:"Path to project definition file." default:"upbound.yaml"`

	// TODO(@tnthornton) remove cacheDir flag. Having a user supplied flag
	// can result in broken behavior between xpls and dep. CacheDir should
	// only be supplied by the Config.
	CacheDir string `short:"d" help:"Directory used for caching package images." default:"~/.up/cache/" env:"CACHE_DIR" type:"path"`
}

// AfterApply constructs and binds Upbound-specific context to any subcommands
// that have Run() methods that receive it.
func (c *addCmd) AfterApply(kongCtx *kong.Context, p pterm.TextPrinter) error {
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
		workspace.WithPrinter(p),
		workspace.WithPermissiveParser(),
	)
	if err != nil {
		return err
	}
	c.ws = ws

	if err := ws.Parse(ctx); err != nil {
		return err
	}

	// workaround interfaces not being bindable ref: https://github.com/alecthomas/kong/issues/48
	kongCtx.BindTo(ctx, (*context.Context)(nil))
	return nil
}

// Run executes the dep command.
func (c *addCmd) Run(ctx context.Context, p pterm.TextPrinter, pb *pterm.BulletListPrinter) error {
	_, err := xpkg.ValidDep(c.Package)
	if err != nil {
		return err
	}

	d := dep.New(c.Package)

	ud, _, err := c.m.AddAll(ctx, d)
	if err != nil {
		return errors.Wrapf(err, "in %s", c.Package)
	}
	p.Printfln("%s:%s added to cache", ud.Package, ud.Constraints)

	meta := c.ws.View().Meta()

	if meta != nil {
		// Metadata file exists in the workspace, upsert the new dependency
		// use the user-specified constraints if provided; otherwise, use latest
		if d.Constraints != "" {
			ud.Constraints = d.Constraints
		}
		if err := meta.Upsert(ud); err != nil {
			return err
		}

		if err := c.ws.Write(meta); err != nil {
			return err
		}
	}
	p.Printfln("%s:%s added to project dependency", ud.Package, ud.Constraints)
	return nil
}
