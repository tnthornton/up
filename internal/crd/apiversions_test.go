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

import "testing"

func TestIsKnownAPIVersion(t *testing.T) {
	KnownAPIVersions = []string{"v1", "v1alpha1", "v1beta1"}

	tests := []struct {
		name     string
		segment  string
		expected bool
	}{
		{
			name:     "KnownVersionV1",
			segment:  "v1",
			expected: true,
		},
		{
			name:     "KnownVersionV1alpha1",
			segment:  "v1alpha1",
			expected: true,
		},
		{
			name:     "UnknownVersion",
			segment:  "v2alpha1",
			expected: false,
		},
		{
			name:     "EmptySegment",
			segment:  "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsKnownAPIVersion(tt.segment)
			if got != tt.expected {
				t.Errorf("isKnownAPIVersion() = %v, want %v", got, tt.expected)
			}
		})
	}
}
