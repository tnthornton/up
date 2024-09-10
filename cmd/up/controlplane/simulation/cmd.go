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

package simulation

import (
	"github.com/alecthomas/kong"
	"github.com/pkg/errors"
	kruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	spacesv1alpha1 "github.com/upbound/up-sdk-go/apis/spaces/v1alpha1"
	spacesv1beta1 "github.com/upbound/up-sdk-go/apis/spaces/v1beta1"

	"github.com/upbound/up/internal/feature"
	"github.com/upbound/up/internal/upbound"
)

func init() {
	kruntime.Must(spacesv1alpha1.AddToScheme(scheme.Scheme))
	kruntime.Must(spacesv1beta1.AddToScheme(scheme.Scheme))
}

// BeforeReset is the first hook to run.
func (c *Cmd) BeforeReset(p *kong.Path, maturity feature.Maturity) error {
	return feature.HideMaturity(p, maturity)
}

// AfterApply constructs and binds a control plane client to any subcommands
// that have Run() methods that receive it.
func (c *Cmd) AfterApply(kongCtx *kong.Context) error {
	upCtx, err := upbound.NewFromFlags(c.Flags)
	if err != nil {
		return err
	}
	kongCtx.Bind(upCtx)

	// we can't use control planes from inside a control plane
	if _, ctp, isSpace := upCtx.GetCurrentSpaceContextScope(); isSpace && ctp.Name != "" {
		return errors.New("cannot access simulations from inside a control plane context. Use 'up ctx ..' to go up to the group context")
	}

	cl, err := upCtx.BuildCurrentContextClient()
	if err != nil {
		return errors.Wrap(err, "unable to get kube client")
	}
	kongCtx.BindTo(cl, (*client.Client)(nil))

	return nil
}

// Cmd contains commands for interacting with control planes.
type Cmd struct {
	Create createCmd `cmd:"" help:"Start a new control plane simulation and wait for the results."`

	// Common Upbound API configuration
	Flags upbound.Flags `embed:""`
}

func (c *Cmd) Help() string {
	return `
Manage control plane simulations. Simulations allow you to "simulate" what
happens on the control plane and see what would changes after the changes are
applied.
`
}