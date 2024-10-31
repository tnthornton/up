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

package run

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	commonv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	xpkgv1 "github.com/crossplane/crossplane/apis/pkg/v1"
	xpkgv1beta1 "github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/cache"
	"github.com/pterm/pterm"
	"github.com/spf13/afero"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/scheme"

	spacesv1beta1 "github.com/upbound/up-sdk-go/apis/spaces/v1beta1"
	ctxcmd "github.com/upbound/up/cmd/up/ctx"
	"github.com/upbound/up/cmd/up/project/common"
	"github.com/upbound/up/internal/async"
	"github.com/upbound/up/internal/profile"
	"github.com/upbound/up/internal/project"
	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/upterm"
	"github.com/upbound/up/internal/xpkg/functions"
	"github.com/upbound/up/internal/xpkg/schemarunner"
	"github.com/upbound/up/pkg/apis/project/v1alpha1"
)

const (
	// TODO(adamwg): Once the package manager changes are in a release channel,
	// we can stop manually setting the version.
	devCrossplaneVersion = "1.18.0-up.1.rc.0.5.gb90fb80"
	// TODO(adamwg): It would be nice if we had a const for this somewhere else.
	devControlPlaneClass = "small"
)

var ctpSchemeBuilders = []*scheme.Builder{
	xpkgv1.SchemeBuilder,
	xpkgv1beta1.SchemeBuilder,
}

type Cmd struct {
	ProjectFile       string        `short:"f" help:"Path to project definition file." default:"upbound.yaml"`
	Repository        string        `optional:"" help:"Repository for the built package. Overrides the repository specified in the project file."`
	NoBuildCache      bool          `help:"Don't cache image layers while building." default:"false"`
	BuildCacheDir     string        `help:"Path to the build cache directory." type:"path" default:"~/.up/build-cache"`
	MaxConcurrency    uint          `help:"Maximum number of functions to build and push at once." env:"UP_MAX_CONCURRENCY" default:"8"`
	ControlPlaneGroup string        `help:"The control plane group that the control plane to use is contained in. This defaults to the group specified in the current context."`
	ControlPlaneName  string        `help:"Name of the control plane to use. It will be created if not found. Defaults to the project name."`
	Flags             upbound.Flags `embed:""`

	projFS             afero.Fs
	functionIdentifier functions.Identifier
	schemaRunner       schemarunner.SchemaRunner
	transport          http.RoundTripper
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

	c.functionIdentifier = functions.DefaultIdentifier
	c.schemaRunner = schemarunner.RealSchemaRunner{}
	c.transport = http.DefaultTransport

	// Set the control plane group based on the current kubeconfig conteext. If
	// there's no namespace specified there, use "default".
	if c.ControlPlaneGroup == "" {
		kubectx, _, _, ok := upCtx.GetCurrentContext()
		if !ok {
			return errors.New("failed to get kubeconfig context")
		}
		if kubectx.Namespace != "" {
			c.ControlPlaneGroup = kubectx.Namespace
		} else {
			c.ControlPlaneGroup = "default"
		}
	}

	pterm.EnableStyling()

	return nil
}

func (c *Cmd) Run(ctx context.Context, upCtx *upbound.Context, p pterm.TextPrinter) error { //nolint:gocyclo // Yeah, we're doing a lot here.
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 1
	}

	var proj *v1alpha1.Project
	err := upterm.WrapWithSuccessSpinner(
		"Parsing project metadata",
		upterm.CheckmarkSuccessSpinner,
		func() error {
			projFilePath := filepath.Join("/", filepath.Base(c.ProjectFile))
			lproj, err := project.Parse(c.projFS, projFilePath)
			if err != nil {
				return errors.Wrap(err, "failed to parse project metadata")
			}
			proj = lproj
			return nil
		},
	)
	if err != nil {
		return err
	}

	if c.Repository != "" {
		proj.Spec.Repository = c.Repository
	}
	if c.ControlPlaneName == "" {
		c.ControlPlaneName = proj.Name
	}

	b := project.NewBuilder(
		project.BuildWithMaxConcurrency(c.MaxConcurrency),
		project.BuildWithFunctionIdentifier(c.functionIdentifier),
		project.BuildWithSchemaRunner(c.schemaRunner),
	)

	var (
		imgMap       project.ImageTagMap
		devCtpClient client.Client
	)
	err = async.WrapWithSuccessSpinners(func(ch async.EventChannel) error {
		eg, ctx := errgroup.WithContext(ctx)

		eg.Go(func() error {
			var err error
			devCtpClient, err = c.ensureControlPlane(ctx, upCtx, ch)
			return err
		})

		eg.Go(func() error {
			var err error
			imgMap, err = b.Build(ctx, proj, c.projFS,
				project.BuildWithEventChannel(ch),
				project.BuildWithImageLabels(common.ImageLabels(c)),
			)
			return err
		})

		return eg.Wait()
	})
	if err != nil {
		return err
	}

	if !c.NoBuildCache {
		// Create a layer cache so that if we're building on top of base images we
		// only pull their layers once. Note we do this here rather than in the
		// builder because pulling layers is deferred to where we use them, which is
		// here.
		cch := cache.NewFilesystemCache(c.BuildCacheDir)
		for tag, img := range imgMap {
			imgMap[tag] = cache.Image(img, cch)
		}
	}

	pusher := project.NewPusher(
		project.PushWithUpboundContext(upCtx),
		project.PushWithTransport(c.transport),
		project.PushWithMaxConcurrency(c.MaxConcurrency),
	)

	var generatedTag name.Tag
	err = async.WrapWithSuccessSpinners(func(ch async.EventChannel) error {
		opts := []project.PushOption{
			project.PushWithEventChannel(ch),
		}

		var err error
		generatedTag, err = pusher.Push(ctx, proj, imgMap, opts...)
		return err
	})
	if err != nil {
		return err
	}

	err = c.installPackage(ctx, devCtpClient, proj, generatedTag)
	if err != nil {
		return err
	}

	return nil
}

func (c *Cmd) ensureControlPlane(ctx context.Context, upCtx *upbound.Context, ch async.EventChannel) (client.Client, error) {
	cl, err := upCtx.BuildCurrentContextClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kube client")
	}

	var ctp spacesv1beta1.ControlPlane
	nn := types.NamespacedName{
		Namespace: c.ControlPlaneGroup,
		Name:      c.ControlPlaneName,
	}
	err = cl.Get(ctx, nn, &ctp)

	switch {
	case err == nil:
		// Make sure it's a dev control plane and not being deleted.
		if ctp.Spec.Class != "small" {
			return nil, errors.New("control plane exists but is not a development control plane")
		}
		if ctp.DeletionTimestamp != nil {
			return nil, errors.New("control plane exists but is being deleted - retry after it finishes deleting")
		}

	case kerrors.IsNotFound(err):
		// Create a control plane.
		if err := c.createControlPlane(ctx, cl, ch); err != nil {
			return nil, err
		}

	default:
		// Unexpected error.
		return nil, errors.Wrap(err, "failed to check for control plane existence")
	}

	ctpClient, err := getControlPlaneClient(ctx, upCtx, nn)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get client for development control plane")
	}

	return ctpClient, nil
}

// getControlPlaneConfig gets a REST config for a given control plane within
// the space.
//
// TODO(adamwg): Mostly copied from simulations; this should be factored out
// into our kube package.
func getControlPlaneClient(ctx context.Context, upCtx *upbound.Context, ctp types.NamespacedName) (client.Client, error) { //nolint:gocyclo
	po := clientcmd.NewDefaultPathOptions()
	var err error

	conf, err := po.GetStartingConfig()
	if err != nil {
		return nil, err
	}
	state, err := ctxcmd.DeriveState(ctx, upCtx, conf, profile.GetIngressHost)
	if err != nil {
		return nil, err
	}

	var ok bool
	var space *ctxcmd.Space

	if space, ok = state.(*ctxcmd.Space); !ok {
		if group, ok := state.(*ctxcmd.Group); ok {
			space = &group.Space
		} else if ctp, ok := state.(*ctxcmd.ControlPlane); ok {
			space = &ctp.Group.Space
		} else {
			return nil, errors.New("current kubeconfig is not pointed at a space cluster")
		}
	}

	spaceClient, err := space.BuildClient(upCtx, ctp)
	if err != nil {
		return nil, err
	}

	kubeconfig, err := spaceClient.ClientConfig()
	if err != nil {
		return nil, err
	}

	ctpClient, err := client.New(kubeconfig, client.Options{})
	if err != nil {
		return nil, err
	}

	for _, bld := range ctpSchemeBuilders {
		if err := bld.AddToScheme(ctpClient.Scheme()); err != nil {
			return nil, err
		}
	}

	return ctpClient, nil
}

func (c *Cmd) createControlPlane(ctx context.Context, cl client.Client, ch async.EventChannel) error {
	evText := "Creating development control plane"
	ch.SendEvent(evText, async.EventStatusStarted)
	ctp := spacesv1beta1.ControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.ControlPlaneName,
			Namespace: c.ControlPlaneGroup,
		},
		Spec: spacesv1beta1.ControlPlaneSpec{
			Crossplane: spacesv1beta1.CrossplaneSpec{
				AutoUpgradeSpec: &spacesv1beta1.CrossplaneAutoUpgradeSpec{
					Channel: ptr.To(spacesv1beta1.CrossplaneUpgradeNone),
				},
				Version: ptr.To(devCrossplaneVersion),
			},
			Class: devControlPlaneClass,
		},
	}
	if err := cl.Create(ctx, &ctp); err != nil {
		ch.SendEvent(evText, async.EventStatusFailure)
		return errors.Wrap(err, "failed to create control plane")
	}

	nn := types.NamespacedName{
		Namespace: c.ControlPlaneGroup,
		Name:      c.ControlPlaneName,
	}
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (done bool, err error) {
		err = cl.Get(ctx, nn, &ctp)
		if err != nil {
			return false, err
		}

		cond := ctp.Status.GetCondition(commonv1.TypeReady)
		return cond.Status == corev1.ConditionTrue, nil
	})
	if err != nil {
		ch.SendEvent(evText, async.EventStatusFailure)
		return errors.Wrap(err, "waiting for control plane to be ready")
	}

	ch.SendEvent(evText, async.EventStatusSuccess)

	return nil
}

func (c *Cmd) installPackage(ctx context.Context, cl client.Client, proj *v1alpha1.Project, tag name.Tag) error {
	pkgSource := tag.String()
	cfg := &xpkgv1.Configuration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: xpkgv1.SchemeGroupVersion.String(),
			Kind:       xpkgv1.ConfigurationKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: proj.Name,
		},
		Spec: xpkgv1.ConfigurationSpec{
			PackageSpec: xpkgv1.PackageSpec{
				Package: pkgSource,
			},
		},
	}

	err := upterm.WrapWithSuccessSpinner(
		"Installing package on development control plane",
		upterm.CheckmarkSuccessSpinner,
		func() error {
			return cl.Patch(ctx, cfg, client.Apply, client.ForceOwnership, client.FieldOwner("up-project-run"))
		},
	)
	if err != nil {
		return err
	}

	err = upterm.WrapWithSuccessSpinner(
		"Waiting for package to be ready",
		upterm.CheckmarkSuccessSpinner,
		waitForPackagesReady(ctx, cl, tag),
	)
	if err != nil {
		return err
	}

	return nil
}

func waitForPackagesReady(ctx context.Context, cl client.Client, tag name.Tag) func() error {
	return func() error {
		nn := types.NamespacedName{
			Name: "lock",
		}
		var lock xpkgv1beta1.Lock
		for {
			time.Sleep(500 * time.Millisecond)
			err := cl.Get(ctx, nn, &lock)
			if err != nil {
				return err
			}

			cfgPkg, cfgFound := lookupLockPackage(lock.Packages, tag.Repository.String(), "")
			if !cfgFound {
				// Configuration not in lock yet.
				continue
			}
			healthy, err := packageIsHealthy(ctx, cl, cfgPkg)
			if err != nil {
				return err
			}
			if !healthy {
				// Configuration is not healthy yet.
				continue
			}

			healthy, err = allDepsHealthy(ctx, cl, lock, cfgPkg)
			if err != nil {
				return err
			}
			if healthy {
				break
			}
		}
		return nil
	}
}

func allDepsHealthy(ctx context.Context, cl client.Client, lock xpkgv1beta1.Lock, pkg xpkgv1beta1.LockPackage) (bool, error) {
	for _, dep := range pkg.Dependencies {
		depPkg, found := lookupLockPackage(lock.Packages, dep.Package, dep.Constraints)
		if !found {
			// Dep is not in lock yet - no need to look at the rest.
			break
		}
		healthy, err := packageIsHealthy(ctx, cl, depPkg)
		if err != nil {
			return false, err
		}
		if !healthy {
			return false, nil
		}
	}

	return true, nil
}

func lookupLockPackage(pkgs []xpkgv1beta1.LockPackage, source, version string) (xpkgv1beta1.LockPackage, bool) {
	for _, pkg := range pkgs {
		if pkg.Source == source {
			if version == "" || pkg.Version == version {
				return pkg, true
			}
		}
	}
	return xpkgv1beta1.LockPackage{}, false
}

func packageIsHealthy(ctx context.Context, cl client.Client, lpkg xpkgv1beta1.LockPackage) (bool, error) {
	var pkg xpkgv1.PackageRevision
	switch lpkg.Type {
	case xpkgv1beta1.ConfigurationPackageType:
		pkg = &xpkgv1.ConfigurationRevision{}

	case xpkgv1beta1.ProviderPackageType:
		pkg = &xpkgv1.ProviderRevision{}

	case xpkgv1beta1.FunctionPackageType:
		pkg = &xpkgv1.FunctionRevision{}
	}

	err := cl.Get(ctx, types.NamespacedName{Name: lpkg.Name}, pkg)
	if err != nil {
		return false, err
	}

	return resource.IsConditionTrue(pkg.GetCondition(commonv1.TypeHealthy)), nil
}
