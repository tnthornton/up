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

package xpls

import (
	"context"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/sourcegraph/jsonrpc2"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/upbound/up/internal/xpls"
)

// serveCmd starts the language server.
type serveCmd struct {
	Cache   string `default:".up/cache" help:"Directory path for dependency schema cache."`
	Verbose bool   `help:"Run server with verbose logging."`
}

// Run runs the language server.
func (c *serveCmd) Run() error {
	// TODO(hasheddan): move to AfterApply.
	zl := zap.New(zap.UseDevMode(c.Verbose))
	h, err := xpls.NewHandler(xpls.WithCacheDir(c.Cache), xpls.WithLogger(logging.NewLogrLogger(zl.WithName("xpls"))))
	if err != nil {
		return err
	}
	// TODO(hasheddan): handle graceful shutdown.
	<-jsonrpc2.NewConn(context.Background(), jsonrpc2.NewBufferedStream(xpls.StdRWC{}, jsonrpc2.VSCodeObjectCodec{}), h).DisconnectNotify()
	return nil
}