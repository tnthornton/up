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

package ctx

import (
	"fmt"

	"k8s.io/apimachinery/pkg/types"

	"github.com/upbound/up/internal/upbound"
)

const (
	upboundPreviousContextSuffix = "-previous"
)

// Accept upserts the "upbound" kubeconfig context and cluster to the chosen
// kubeconfig, pointing to the space.
func (s *Space) Accept(upCtx *upbound.Context, navCtx *navContext) (msg string, err error) {
	config, err := s.BuildClient(upCtx, types.NamespacedName{})
	if err != nil {
		return "", err
	}
	raw, err := config.RawConfig()
	if err != nil {
		return "", err
	}
	if err := navCtx.contextWriter.Write(&raw); err != nil {
		return "", err
	}

	return fmt.Sprintf(contextSwitchedFmt, withUpboundPrefix(s.Breadcrumbs())), nil
}

// Accept upserts the "upbound" kubeconfig context and cluster to the chosen
// kubeconfig, pointing to the group.
func (g *Group) Accept(upCtx *upbound.Context, navCtx *navContext) (msg string, err error) {
	config, err := g.Space.BuildClient(upCtx, types.NamespacedName{Namespace: g.Name})
	if err != nil {
		return "", err
	}
	raw, err := config.RawConfig()
	if err != nil {
		return "", err
	}
	if err := navCtx.contextWriter.Write(&raw); err != nil {
		return "", err
	}

	return fmt.Sprintf(contextSwitchedFmt, withUpboundPrefix(g.Breadcrumbs())), nil
}

// Accept upserts a controlplane context and cluster to the chosen kubeconfig.
func (ctp *ControlPlane) Accept(upCtx *upbound.Context, navCtx *navContext) (msg string, err error) {
	config, err := ctp.Group.Space.BuildClient(upCtx, ctp.NamespacedName())
	if err != nil {
		return "", err
	}
	raw, err := config.RawConfig()
	if err != nil {
		return "", err
	}
	if err := navCtx.contextWriter.Write(&raw); err != nil {
		return "", err
	}

	return fmt.Sprintf(contextSwitchedFmt, withUpboundPrefix(ctp.Breadcrumbs())), nil
}
