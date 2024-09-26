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

package functions

import (
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
)

func TestIdentify(t *testing.T) {
	tcs := map[string]struct {
		files           map[string]string
		expectError     bool
		expectedBuilder Builder
	}{
		"DockerfileOnly": {
			files: map[string]string{
				"Dockerfile": "FROM scratch",
			},
			expectedBuilder: &dockerBuilder{},
		},
		"KCLOnly": {
			files: map[string]string{
				"kcl.mod": "[package]",
			},
			expectedBuilder: &kclBuilder{},
		},
		"DockerfileAndKCL": {
			files: map[string]string{
				"Dockerfile": "FROM scratch",
				"kcl.mod":    "[package]",
			},
			// dockerBuilder has precedence.
			expectedBuilder: &dockerBuilder{},
		},
		"Empty": {
			files:       make(map[string]string),
			expectError: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fromFS := afero.NewMemMapFs()
			for fname, content := range tc.files {
				err := afero.WriteFile(fromFS, fname, []byte(content), 0644)
				assert.NilError(t, err)
			}

			builder, err := DefaultIdentifier.Identify(fromFS)
			if tc.expectError {
				assert.Error(t, err, errNoSuitableBuilder)
			} else {
				wantType := reflect.TypeOf(tc.expectedBuilder)
				gotType := reflect.TypeOf(builder)
				assert.Equal(t, wantType, gotType)
			}
		})
	}
}
