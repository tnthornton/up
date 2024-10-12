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

package crd

import (
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	xpv1 "github.com/crossplane/crossplane/apis/apiextensions/v1"
	"github.com/crossplane/crossplane/xcrd"
	"github.com/spf13/afero"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"sigs.k8s.io/yaml"
)

var (
	crdGVK = apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
)

// createCRDFromXRD creates a xrCRD and claimCRD if possible from the XRD
func createCRDFromXRD(xrd xpv1.CompositeResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, *apiextensionsv1.CustomResourceDefinition, error) {
	var xrCrd, claimCrd *apiextensionsv1.CustomResourceDefinition

	xrCrd, err := xcrd.ForCompositeResource(&xrd)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q for Composite Resource Claim", xrd.GetName())
	}
	if xrCrd != nil {
		xrCrd.SetGroupVersionKind(crdGVK)
	}

	if xrd.Spec.ClaimNames != nil {
		claimCrd, err = xcrd.ForCompositeResourceClaim(&xrd)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "cannot derive composite CRD from XRD %q for Composite Resource", xrd.GetName())
		}
	}
	if claimCrd != nil {
		claimCrd.SetGroupVersionKind(crdGVK)
	}

	// Return the derived CRDs
	return claimCrd, xrCrd, nil
}

// ProcessXRD generate associated CRDs
func ProcessXRD(fs afero.Fs, bs []byte, path, baseFolder string) (string, string, error) {
	var xrd xpv1.CompositeResourceDefinition
	if err := yaml.Unmarshal(bs, &xrd); err != nil {
		return "", "", errors.Wrapf(err, "failed to unmarshal XRD file %q", path)
	}

	// Create CRDs from the XRD
	xrCRD, claimCRD, err := createCRDFromXRD(xrd)
	if err != nil {
		return "", "", err
	}

	var xrPath, claimPath string

	// Write the XR CRD file if it exists
	if xrCRD != nil {
		xrPath = filepath.Join(baseFolder, path+"-xr.yaml")
		xrCRDBytes, err := yaml.Marshal(xrCRD)
		if err != nil {
			return "", "", errors.Wrap(err, "failed to marshal XR CRD to YAML")
		}
		if err := afero.WriteFile(fs, xrPath, xrCRDBytes, 0o644); err != nil {
			return "", "", err
		}
	}

	// Write the Claim CRD file if it exists
	if claimCRD != nil {
		claimPath = filepath.Join(baseFolder, path+"-claim.yaml")
		claimCRDBytes, err := yaml.Marshal(claimCRD)
		if err != nil {
			return "", "", errors.Wrap(err, "failed to marshal claim CRD to YAML")
		}
		if err := afero.WriteFile(fs, claimPath, claimCRDBytes, 0o644); err != nil {
			return "", "", err
		}
	}

	// Return the paths of the files created, or empty strings if they were not created
	return xrPath, claimPath, nil
}
