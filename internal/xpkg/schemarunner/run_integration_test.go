// Copyright 2024 Upbound Inc
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

//go:build integration
// +build integration

package schemarunner

import (
	"context"
	_ "embed"
	"os"
	"testing"

	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
)

const (
	kclImage = "kcllang/kcl:v0.10.6"
)

func TestRunContainerWithKCLIntegration(t *testing.T) {
	type withFsFn func() afero.Fs

	type args struct {
		baseFolder string
		imageName  string
		command    []string
		fs         withFsFn
	}

	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SuccessWithAccountScaffoldDefinition": {
			reason: "Should successfully run container with crd.",
			args: args{
				baseFolder: "data/input", // Use relative path here
				imageName:  kclImage,
				command: []string{
					"sh", "-c",
					`find . -name "*.yaml" -exec kcl import -m crd -s {} \;`,
				},
				fs: func() afero.Fs {
					fs := afero.NewMemMapFs()

					// Use relative paths for directory and file creation
					_ = fs.Mkdir("data/input", os.ModePerm)
					_ = afero.WriteFile(fs, "data/input/template.fn.crossplane.io_kclinputs.yaml", crd, os.ModePerm)

					return fs
				},
			},
			want: want{
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := tc.args.fs()

			schemaRunner := RealSchemaRunner{}
			ctx := context.Background()
			err := schemaRunner.Generate(ctx, fs, tc.args.baseFolder, tc.args.imageName, tc.args.command)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nRunContainer(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			outputExists, _ := afero.Exists(fs, "models/k8s/apimachinery/pkg/apis/meta/v1/managed_fields_entry.k")
			if !outputExists {
				t.Errorf("\n%s\nExpected output file not found in in-memory fs", tc.reason)
			}
		})
	}
}
