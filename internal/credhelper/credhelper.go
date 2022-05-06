// Copyright 2022 Upbound Inc
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

package credhelper

import (
	"fmt"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/pkg/errors"

	"github.com/upbound/up/internal/config"
)

const (
	errUnimplemented     = "operation is not implemented"
	errInitializeSource  = "unable to initialize source"
	errExtractConfig     = "unable to extract config"
	errGetDefaultProfile = "unable to get default profile in config"
	errGetProfile        = "unable to get specified profile in config"
	errNotSupported      = "serverURL is not supported by helper"
)

const (
	defaultDockerUser = "_token"
	defaultEndpoint   = "upbound.io"
)

// Helper is a docker credential helper for Upbound.
type Helper struct {
	log logging.Logger

	endpoint string
	profile  string
	src      config.Source
}

// Opt sets a helper option.
type Opt func(h *Helper)

// WithEndpoint sets the helper endpoint.
func WithEndpoint(e string) Opt {
	return func(h *Helper) {
		h.endpoint = e
	}
}

// WithLogger sets the helper logger.
func WithLogger(l logging.Logger) Opt {
	return func(h *Helper) {
		h.log = l
	}
}

// WithProfile sets the helper profile.
func WithProfile(p string) Opt {
	return func(h *Helper) {
		h.profile = p
	}
}

// WithSource sets the source for the helper config.
func WithSource(src config.Source) Opt {
	return func(h *Helper) {
		h.src = src
	}
}

// New constructs a new Docker credential helper.
func New(opts ...Opt) *Helper {
	h := &Helper{
		log: logging.NewNopLogger(),
		src: config.NewFSSource(),
	}

	for _, o := range opts {
		o(h)
	}

	return h
}

// Add adds the supplied credentials.
func (h *Helper) Add(c *credentials.Credentials) error {
	return errors.New(errUnimplemented)
}

// Delete deletes credentials for the supplied server.
func (h *Helper) Delete(serverURL string) error {
	return errors.New(errUnimplemented)
}

// List lists all the configured credentials.
func (h *Helper) List() (map[string]string, error) {
	return nil, errors.New(errUnimplemented)
}

// Get gets credentials for the supplied server.
func (h *Helper) Get(serverURL string) (string, string, error) {
	// check if serverURL is an upbound.io URL OR if the serverURL matches
	// the configured target endpoint.
	if !strings.Contains(serverURL, defaultEndpoint) && h.endpoint == "" {
		return "", "", fmt.Errorf(errNotSupported)
	}
	if h.endpoint != "" && serverURL != h.endpoint {
		return "", "", fmt.Errorf(errNotSupported)
	}

	if err := h.src.Initialize(); err != nil {
		return "", "", errors.Wrap(err, errInitializeSource)
	}
	conf, err := config.Extract(h.src)
	if err != nil {
		return "", "", errors.Wrap(err, errExtractConfig)
	}
	var p config.Profile
	if h.profile == "" {
		_, p, err = conf.GetDefaultUpboundProfile()
		if err != nil {
			return "", "", errors.Wrap(err, errGetDefaultProfile)
		}
	} else {
		p, err = conf.GetUpboundProfile(h.profile)
		if err != nil {
			return "", "", errors.Wrap(err, errGetProfile)
		}
	}
	return defaultDockerUser, p.Session, nil
}
