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

package migration

import (
	"testing"
)

func TestIsAllowedImportTarget(t *testing.T) {
	type args struct {
		host string
	}
	tests := map[string]struct {
		reason  string
		args    args
		allowed bool
	}{
		"New": {
			reason: "Should match with the new format",
			args: args{
				host: "https://00.000.000.0.nip.io/apis/spaces.upbound.io/v1beta1/namespaces/default/controlplanes/ctp1/k8s",
			},
			allowed: true,
		},
		"OldLower": {
			reason: "Should match with the old format with lowercase controplanes",
			args: args{
				host: "https://spaces-foo.upboundrocks.cloud/v1/controlplanes/acmeco/default/ctp/k8s",
			},
			allowed: true,
		},
		"OldWithDifferentNames": {
			reason: "Should match with the old format with lowercase controplanes, even with a different account/ctp name",
			args: args{
				host: "https://spaces-foo.upboundrocks.cloud/v1/controlplanes/mycompany/default/ctp1/k8s",
			},
			allowed: true,
		},
		"OldCamelCase": {
			reason: "Should match with the old format with camelcase controlPlanes",
			args: args{
				host: "https://spaces-foo.upboundrocks.cloud/v1/controlPlanes/acmeco/default/ctp/k8s",
			},
			allowed: true,
		},
		"LocalHostCase": {
			reason: "Should match the localhost format with high port.",
			args: args{
				host: "https://127.0.0.1:56613",
			},
			allowed: true,
		},
		"OthersNotAllowed": {
			reason: "Should not match controlPlanes or localhost format",
			args: args{
				host: "https://6D24B1990F515F5A1A1E4E232DC73B96.gr7.us-west-2.eks.amazonaws.com",
			},
			allowed: false,
		},
		"MoreOthersNotAllowed": {
			reason: "Should not match controlPlanes or localhost format",
			args: args{
				host: "https://azure-spoke-02-dyw3h5dj.hcp.westus.azmk8s.io:443",
			},
			allowed: false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := isAllowedImportTarget(tt.args.host); got != tt.allowed {
				t.Errorf("isAllowedImportTarget() = %v, allowed %v", got, tt.allowed)
			}
		})
	}
}
