// Copyright 2023 Upbound Inc
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

package space

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/blang/semver/v4"
	"github.com/pterm/pterm"
	"helm.sh/helm/v3/pkg/chart"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/feature"

	spacefeature "github.com/upbound/up/cmd/up/space/features"
	"github.com/upbound/up/cmd/up/space/prerequisites"
	"github.com/upbound/up/internal/config"
	"github.com/upbound/up/internal/install"
	"github.com/upbound/up/internal/install/helm"
	"github.com/upbound/up/internal/kube"
	"github.com/upbound/up/internal/upbound"
	"github.com/upbound/up/internal/upterm"
)

const (
	msgUpgrading                   = "Upgrading"
	msgDowngrading                 = "Downgrading"
	errParseUpgradeParameters      = "unable to parse upgrade parameters"
	errFailedGettingCurrentVersion = "failed to retrieve current version"
	errInvalidVersionFmt           = "invalid version %q"
	errAborted                     = "aborted"
	warnDowngrade                  = "Downgrades are not supported."
	warnMajorUpgrade               = "Upgrades to a new major version are only supported for explicitly documented releases."
	warnMinorVersionSkip           = "Upgrades which skip a minor version are not supported."
)

// upgradeCmd upgrades Upbound.
type upgradeCmd struct {
	Kube     upbound.KubeFlags       `embed:""`
	Registry authorizedRegistryFlags `embed:""`
	install.CommonParams
	Upbound upbound.Flags `embed:""`

	// NOTE(hasheddan): version is currently required for upgrade with OCI image
	// as latest strategy is undetermined.
	Version  string `arg:"" help:"Upbound Spaces version to upgrade to."`
	Yes      bool   `name:"yes" type:"bool" help:"Answer yes to all questions"`
	Rollback bool   `help:"Rollback to previously installed version on failed upgrade."`

	helmMgr    install.Manager
	prereqs    *prerequisites.Manager
	helmParams map[string]any
	kClient    kubernetes.Interface
	pullSecret *kube.ImagePullApplicator
	quiet      config.QuietFlag
	features   *feature.Flags
	oldVersion string
	downgrade  bool
}

// BeforeApply sets default values in login before assignment and validation.
func (c *upgradeCmd) BeforeApply() error {
	c.Set = make(map[string]string)
	return nil
}

// AfterApply sets default values in command after assignment and validation.
func (c *upgradeCmd) AfterApply(kongCtx *kong.Context, quiet config.QuietFlag) error { //nolint:gocyclo
	if err := c.Kube.AfterApply(); err != nil {
		return err
	}
	if err := c.Registry.AfterApply(); err != nil {
		return err
	}

	// NOTE(tnthornton) we currently only have support for stylized output.
	pterm.EnableStyling()
	upterm.DefaultObjPrinter.Pretty = true

	upCtx, err := upbound.NewFromFlags(c.Upbound)
	if err != nil {
		return err
	}
	upCtx.SetupLogging()

	kongCtx.Bind(upCtx)

	kubeconfig := c.Kube.GetConfig()

	kClient, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		return err
	}
	c.kClient = kClient

	secret := kube.NewSecretApplicator(kClient)
	c.pullSecret = kube.NewImagePullApplicator(secret)
	mgr, err := helm.NewManager(kubeconfig,
		spacesChart,
		c.Registry.Repository,
		helm.WithNamespace(ns),
		helm.WithBasicAuth(c.Registry.Username, c.Registry.Password),
		helm.IsOCI(),
		helm.WithChart(c.Bundle),
		helm.RollbackOnError(c.Rollback),
		helm.Wait())
	if err != nil {
		return err
	}
	c.helmMgr = mgr

	base := map[string]any{}
	if c.File != nil {
		defer c.File.Close() //nolint:errcheck,gosec
		b, err := io.ReadAll(c.File)
		if err != nil {
			return errors.Wrap(err, errReadParametersFile)
		}
		if err := yaml.Unmarshal(b, &base); err != nil {
			return errors.Wrap(err, errReadParametersFile)
		}
		if err := c.File.Close(); err != nil {
			return errors.Wrap(err, errReadParametersFile)
		}
	}
	parser := helm.NewParser(base, c.Set)
	c.helmParams, err = parser.Parse()
	if err != nil {
		return errors.Wrap(err, errParseInstallParameters)
	}

	// validate versions
	c.oldVersion, err = mgr.GetCurrentVersion()
	if err != nil {
		return errors.Wrap(err, errFailedGettingCurrentVersion)
	}

	if c.Bundle == nil {
		from, err := semver.Parse(c.oldVersion)
		if err != nil {
			return errors.Wrapf(err, errInvalidVersionFmt, c.oldVersion)
		}
		to, err := semver.Parse(strings.TrimPrefix(c.Version, "v"))
		if err != nil {
			return errors.Wrapf(err, errInvalidVersionFmt, c.Version)
		}
		c.downgrade = from.GT(to)

		if err := c.validateVersions(from, to); err != nil {
			return err
		}
	}

	c.features = &feature.Flags{}
	spacefeature.EnableFeatures(c.features, c.helmParams)

	prereqs, err := prerequisites.New(kubeconfig, nil, c.features, c.Version)
	if err != nil {
		return err
	}
	c.prereqs = prereqs

	c.quiet = quiet
	return nil
}

// Run executes the upgrade command.
func (c *upgradeCmd) Run(ctx context.Context) error {
	overrideRegistry(c.Registry.Repository.String(), c.helmParams)

	// check if required prerequisites are installed
	status, err := c.prereqs.Check()
	if err != nil {
		pterm.Error.Println("error checking prerequisites status")
		return err
	}

	// At least 1 prerequisite is not installed, check if we should install the
	// missing ones for the client.
	if len(status.NotInstalled) > 0 {
		pterm.Warning.Printfln("One or more required prerequisites are not installed:")
		pterm.Println()
		for _, p := range status.NotInstalled {
			pterm.Println(fmt.Sprintf("❌ %s", p.GetName()))
		}

		if !c.Yes {
			pterm.Println() // Blank line
			confirm := pterm.DefaultInteractiveConfirm
			confirm.DefaultText = "Would you like to install them now?"
			result, _ := confirm.Show()
			pterm.Println() // Blank line
			if !result {
				pterm.Error.Println("prerequisites must be met in order to proceed with upgrade")
				return nil
			}
		}
		if err := installPrereqs(status); err != nil {
			return err
		}
	}

	pterm.Info.Printfln("Required prerequisites met!")
	pterm.Info.Printfln("Proceeding with Upbound Spaces upgrade...")

	// Create or update image pull secret.

	pullSecret := func() error {
		if err := c.pullSecret.Apply(ctx, defaultImagePullSecret, ns, c.Registry.Username, c.Registry.Password, c.Registry.Endpoint.String()); err != nil {
			return errors.Wrap(err, errCreateImagePullSecret)
		}
		return nil
	}

	if err := upterm.WrapWithSuccessSpinner(
		upterm.StepCounter(fmt.Sprintf("Creating pull secret %s", defaultImagePullSecret), 1, 2),
		upterm.CheckmarkSuccessSpinner,
		pullSecret,
	); err != nil {
		pterm.Println()
		pterm.Println()
		return err
	}

	if err := c.upgradeUpbound(c.helmParams); err != nil {
		return err
	}

	pterm.Info.WithPrefix(upterm.RaisedPrefix).Println("Your Upbound Space is Ready after Upgrade!")

	outputNextSteps()

	return nil
}

func upgradeVersionBounds(_ string, ch *chart.Chart) error {
	return checkVersion(fmt.Sprintf("unsupported target chart version %s", ch.Metadata.Version), upgradeVersionConstraints, ch.Metadata.Version)
}

func upgradeFromVersionBounds(from string, ch *chart.Chart) error {
	return checkVersion(fmt.Sprintf("unsupported installed chart version %s", ch.Metadata.Version), upgradeFromVersionConstraints, from)
}

func upgradeUpVersionBounds(_ string, ch *chart.Chart) error {
	return upVersionBounds(ch)
}

func (c *upgradeCmd) upgradeUpbound(params map[string]any) error {
	version := strings.TrimPrefix(c.Version, "v")
	upgrade := func() error {
		if err := c.helmMgr.Upgrade(version, params, upgradeUpVersionBounds, upgradeFromVersionBounds, upgradeVersionBounds); err != nil {
			return err
		}
		return nil
	}

	verb := msgUpgrading
	if c.downgrade {
		verb = msgDowngrading
	}

	if err := upterm.WrapWithSuccessSpinner(
		upterm.StepCounter(fmt.Sprintf("%s Space from v%s to v%s", verb, c.oldVersion, version), 2, 2),
		upterm.CheckmarkSuccessSpinner,
		upgrade,
	); err != nil {
		pterm.Println()
		pterm.Println()
		return err
	}

	return nil
}

// validateVersions checks whether the upgrade/downgrade is allowed based on version changes.
func (c *upgradeCmd) validateVersions(from, to semver.Version) error {
	var warning string

	// Use switch to centralize message selection logic
	switch {
	case c.downgrade:
		warning = warnDowngrade
	case to.Major > from.Major:
		warning = warnMajorUpgrade
	case to.Minor > from.Minor+1:
		warning = warnMinorVersionSkip
	default:
		// No warning means the validation passed
		return nil
	}

	// If there's a warning, prompt for confirmation
	return warnAndConfirm(warning)
}

// warnAndConfirm displays a warning and prompts for confirmation.
func warnAndConfirm(warning string, args ...any) error {
	pterm.Println()                          // Blank line for better readability
	pterm.Warning.Printfln(warning, args...) // Display the warning message
	pterm.Println()                          // Another blank line

	confirm := pterm.DefaultInteractiveConfirm
	confirm.DefaultText = "Are you sure you want to proceed?"

	if result, _ := confirm.Show(); !result {
		return errors.New(errAborted)
	}

	pterm.Println() // Final blank line
	return nil
}
