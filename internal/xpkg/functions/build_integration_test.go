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

//go:build integration
// +build integration

package functions

import (
	"context"
	"embed"
	"testing"

	"github.com/spf13/afero"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

//go:embed testdata/docker-function/**
var dockerFunction embed.FS

func TestDockerBuild(t *testing.T) {
	t.Parallel()

	b := &dockerBuilder{}
	fromFS := afero.NewBasePathFs(
		afero.FromIOFS{FS: dockerFunction},
		"testdata/docker-function",
	)

	fnImgs, err := b.Build(context.Background(), fromFS, []string{"amd64"}, nil)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(fnImgs, 1))
	fnImg := fnImgs[0]

	// Check that it built the image we asked for.
	cfgFile, err := fnImg.ConfigFile()
	assert.NilError(t, err)
	assert.Equal(t, cfgFile.Architecture, "amd64")
	assert.DeepEqual(t, cfgFile.Config.Entrypoint, []string{"this-is-a-test"})
}
