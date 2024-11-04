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

package yaml

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type objectWithoutMetadata struct {
	FieldA string `json:"fieldA"`
	FieldB string `jsoN:"fieldB"`
}

type metadataWithoutCreationTimestamp struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type objectWithoutCreationTimestamp struct {
	Metadata metadataWithoutCreationTimestamp `json:"metadata"`
}

type objectWithCreationTimestamp struct {
	metav1.ObjectMeta `json:"metadata"`
}

func TestMarshal(t *testing.T) {
	tcs := map[string]struct {
		input        any
		expectedYAML string
	}{
		"NoMetadata": {
			input: &objectWithoutMetadata{
				FieldA: "hello",
				FieldB: "world",
			},
			expectedYAML: `fieldA: hello
fieldB: world
`,
		},
		"NoTimestamp": {
			input: &objectWithoutCreationTimestamp{
				Metadata: metadataWithoutCreationTimestamp{
					Name:      "hello",
					Namespace: "world",
				},
			},
			expectedYAML: `metadata:
  name: hello
  namespace: world
`,
		},
		"NilTimestamp": {
			input: &objectWithCreationTimestamp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hello",
					Namespace: "world",
				},
			},
			expectedYAML: `metadata:
  name: hello
  namespace: world
`,
		},
		"NonNilTimestamp": {
			input: &objectWithCreationTimestamp{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "hello",
					Namespace:         "world",
					CreationTimestamp: metav1.Date(2024, 11, 7, 12, 13, 14, 0, time.UTC),
				},
			},
			expectedYAML: `metadata:
  creationTimestamp: "2024-11-07T12:13:14Z"
  name: hello
  namespace: world
`,
		},
		"NonPointer": {
			input: objectWithCreationTimestamp{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "hello",
					Namespace:         "world",
					CreationTimestamp: metav1.Date(2024, 11, 7, 12, 13, 14, 0, time.UTC),
				},
			},
			expectedYAML: `metadata:
  creationTimestamp: "2024-11-07T12:13:14Z"
  name: hello
  namespace: world
`,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			bs, err := Marshal(tc.input)
			assert.NilError(t, err)
			assert.Equal(t, string(bs), tc.expectedYAML)
		})
	}
}
