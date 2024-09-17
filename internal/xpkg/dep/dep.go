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

package dep

import (
	"strings"

	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/upbound/up/internal/xpkg/dep/resolver/image"
)

// New returns a new v1beta1.Dependency based on the given package name.
// Expects names of the form source@version or source:version where
// the version part can be left blank to indicate 'latest'.
func New(pkg string) v1beta1.Dependency {
	// If the passed-in version was blank, use the default to pass
	// constraint checks and grab the latest semver
	version := image.DefaultVer

	// Split the package into parts by '/'
	parts := strings.Split(pkg, "/")

	// Assume the last part could be the version tag
	lastPart := parts[len(parts)-1]

	// Initialize source with the input package name
	source := pkg

	// Check if the last part contains '@' or ':'
	if strings.ContainsAny(lastPart, "@:") {
		// Find the first occurrence of either '@' or ':'
		var delimiter string
		if at := strings.Index(lastPart, "@"); at != -1 {
			delimiter = "@"
		}
		if colon := strings.LastIndex(lastPart, ":"); colon != -1 {
			// Use the latest delimiter found
			if delimiter == "" || colon > strings.Index(lastPart, delimiter) {
				delimiter = ":"
			}
		}

		if prefix, suffix, found := strings.Cut(lastPart, delimiter); found {
			parts[len(parts)-1] = prefix
			source = strings.Join(parts, "/")
			version = suffix
		}
	}

	return v1beta1.Dependency{
		Package:     source,
		Constraints: version,
	}
}

// NewWithType returns a new v1beta1.Dependency based on the given package
// name and PackageType (represented as a string).
// Expects names of the form source@version where @version can be
// left blank in order to indicate 'latest'.
func NewWithType(pkg string, t string) v1beta1.Dependency {
	d := New(pkg)

	c := cases.Title(language.Und) // Create a caser for title casing
	normalized := c.String(strings.ToLower(t))

	switch normalized {
	case string(v1beta1.ConfigurationPackageType):
		d.Type = v1beta1.ConfigurationPackageType
	case string(v1beta1.FunctionPackageType):
		d.Type = v1beta1.FunctionPackageType
	case string(v1beta1.ProviderPackageType):
		d.Type = v1beta1.ProviderPackageType
	}

	return d
}
