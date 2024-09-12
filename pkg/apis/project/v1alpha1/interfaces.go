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

package v1alpha1

import pkgmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"

// GetCrossplaneConstraints gets the Project's Crossplane version constraints.
func (p *Project) GetCrossplaneConstraints() *pkgmetav1.CrossplaneConstraints {
	return p.Spec.Crossplane
}

// GetDependencies gets the Project's dependencies.
func (p *Project) GetDependencies() []pkgmetav1.Dependency {
	return p.Spec.DependsOn
}
