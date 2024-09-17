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

package xpkg

import (
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	defaultVer = "latest"
)

var errInvalidPkgName = errors.New("invalid package dependency supplied")

// ValidDep --
func ValidDep(pkg string) (bool, error) {
	upkg := strings.ReplaceAll(pkg, "@", ":")

	_, err := parsePackageReference(upkg)
	if err != nil {
		return false, errors.Errorf("%s: %s", errInvalidPkgName.Error(), err.Error())
	}

	return true, nil
}

func parsePackageReference(pkg string) (bool, error) { // nolint:gocyclo
	if pkg == "" {
		return false, errors.Errorf("could not parse reference: empty package name, %s", errInvalidPkgName.Error())
	}

	version := defaultVer
	var source string
	parts := strings.Split(pkg, "/")
	lastPart := parts[len(parts)-1]

	if strings.ContainsAny(lastPart, "@:") {
		var delimiter string
		if at := strings.Index(lastPart, "@"); at != -1 {
			delimiter = "@"
		}
		if colon := strings.LastIndex(lastPart, ":"); colon != -1 {
			if delimiter == "" || colon > strings.Index(lastPart, delimiter) {
				delimiter = ":"
			}
		}

		source = pkg
		if prefix, suffix, found := strings.Cut(lastPart, delimiter); found {
			parts[len(parts)-1] = prefix
			source = strings.Join(parts, "/")
			version = suffix
		}
	} else {
		source = pkg
	}

	_, err := name.ParseReference(source)
	if err != nil {
		return false, errors.Errorf("%s: %s", errInvalidPkgName.Error(), err.Error())
	}

	if version != defaultVer {
		_, err := semver.NewConstraint(version)
		if err != nil {
			return false, errors.Errorf("invalid SemVer constraint %s: %s", version, errInvalidPkgName.Error())
		}
	}

	return true, nil
}
