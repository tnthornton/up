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
	"context"
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/pkg/errors"
	"github.com/pterm/pterm"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	spacesv1alpha1 "github.com/upbound/up-sdk-go/apis/spaces/v1alpha1"
	"github.com/upbound/up/internal/upbound"
)

// deleteCmd deletes a simulation on Upbound.
type deleteCmd struct {
	Name  string `arg:"" help:"Name of the simulation." predictor:"ctps"`
	Group string `short:"g" default:"" help:"The group that the simulation is contained in. This defaults to the group specified in the current context"`
}

// AfterApply sets default values in command after assignment and validation.
func (c *deleteCmd) AfterApply(kongCtx *kong.Context, upCtx *upbound.Context) error {
	if c.Group == "" {
		ns, _, err := upCtx.Kubecfg.Namespace()
		if err != nil {
			return err
		}
		c.Group = ns
	}
	return nil
}

// Run executes the delete command.
func (c *deleteCmd) Run(ctx context.Context, p pterm.TextPrinter, upCtx *upbound.Context, client client.Client) error {
	ctp := &spacesv1alpha1.Simulation{
		ObjectMeta: v1.ObjectMeta{
			Name:      c.Name,
			Namespace: c.Group,
		},
	}

	if err := client.Delete(ctx, ctp); err != nil {
		if kerrors.IsNotFound(err) {
			return fmt.Errorf("simulation %q not found", c.Name)
		}
		return errors.Wrap(err, "error deleting simulation")
	}
	p.Printfln("%s deleted", c.Name)
	return nil
}
