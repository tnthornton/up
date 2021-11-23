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
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	apimetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"

	"github.com/crossplane/crossplane-runtime/pkg/test"
	xpmetav1alpha1 "github.com/crossplane/crossplane/apis/pkg/meta/v1alpha1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"

	"github.com/upbound/up/internal/xpkg"
	"github.com/upbound/up/internal/xpkg/parser"
)

const (
	testProviderPkgYaml = "testdata/provider_package.yaml"

	testProviderConfigsCRD      = "providerconfigs.helm.crossplane.io.yaml"
	testProviderConfigUsagesCRD = "providerconfigusages.helm.crossplane.io.yaml"
	testDigestFile              = "sha256:295bcd0e6dc396cf0f5ef638c8a7610a571ff2dcef3aa0447398f25b5a0eafc7"
	testMetaFile                = "crossplane.yaml"
)

func TestFromImage(t *testing.T) {
	type args struct {
		reg string
		tag string
		img v1.Image
	}

	type want struct {
		pkg        *ParsedPackage
		numObjects int
		err        error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Should return a ParsedPackage and no error.",
			args: args{
				reg: "index.docker.io/crossplane/provider-aws",
				tag: "v0.20.0",
				img: newPackageImage(testProviderPkgYaml),
			},
			want: want{
				pkg: &ParsedPackage{
					SHA: digest(newPackageImage(testProviderPkgYaml)),
					Deps: []v1beta1.Dependency{
						{
							Package:     "crossplane/provider-gcp",
							Type:        v1beta1.ProviderPackageType,
							Constraints: "v0.18.0",
						},
					},
					MetaObj: &xpmetav1alpha1.Provider{
						TypeMeta: apimetav1.TypeMeta{
							APIVersion: "meta.pkg.crossplane.io/v1alpha1",
							Kind:       "Provider",
						},
						ObjectMeta: apimetav1.ObjectMeta{
							Name: "provider-aws",
						},
						Spec: xpmetav1alpha1.ProviderSpec{
							Controller: xpmetav1alpha1.ControllerSpec{
								Image: "crossplane/provider-aws-controller:v0.20.0",
							},
							MetaSpec: xpmetav1alpha1.MetaSpec{
								DependsOn: []xpmetav1alpha1.Dependency{
									{
										Provider: pointer.String("crossplane/provider-gcp"),
										Version:  "v0.18.0",
									},
								},
							},
						},
					},
					PType: v1beta1.ProviderPackageType,
					Reg:   "index.docker.io/crossplane/provider-aws",
					Ver:   "v0.20.0",
				},
				numObjects: 2,
			},
		},
		"ErrInvalidPackageImage": {
			reason: "Should return an error if package image is invalid.",
			args: args{
				img: empty.Image,
			},
			want: want{
				err: errors.Wrap(errors.New("open package.yaml: no such file or directory"), errOpenPackageStream),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			parser, _ := parser.New()

			pkgres := NewResolver(WithParser(parser))

			pkg, err := pkgres.FromImage(tc.args.reg, tc.args.tag, tc.args.img)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			if err == nil {

				if diff := cmp.Diff(tc.want.pkg.Digest(), pkg.Digest()); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Dependencies(), pkg.Dependencies()); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Meta(), pkg.Meta()); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.numObjects, len(pkg.Objects())); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Registry(), pkg.Registry()); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Type(), pkg.Type()); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Version(), pkg.Version()); diff != "" {
					t.Errorf("\n%s\nFromImage(...): -want err, +got err:\n%s", tc.reason, diff)
				}
			}
		})
	}
}

func TestFromDir(t *testing.T) {

	inmemfs := afero.NewMemMapFs()
	testdatafs := afero.NewOsFs()
	path := "index.docker.io/crossplane/provider-helm@v0.9.0"

	_ = inmemfs.MkdirAll(path, os.ModePerm)

	// write files to the above path
	meta, _ := testdatafs.Open(filepath.Join("testdata", testMetaFile))
	defer meta.Close()
	crd1, _ := testdatafs.Open(filepath.Join("testdata", testProviderConfigsCRD))
	defer crd1.Close()
	crd2, _ := testdatafs.Open(filepath.Join("testdata", testProviderConfigUsagesCRD))
	defer crd2.Close()
	sha, _ := testdatafs.Open(filepath.Join("testdata", testDigestFile))
	defer sha.Close()

	targetMeta, _ := inmemfs.Create(filepath.Join(path, xpkg.MetaFile))
	targetCRD1, _ := inmemfs.Create(filepath.Join(path, testProviderConfigsCRD))
	targetCRD2, _ := inmemfs.Create(filepath.Join(path, testProviderConfigUsagesCRD))
	targetSHA, _ := inmemfs.Create(filepath.Join(path, testDigestFile))

	io.Copy(targetMeta, meta)
	io.Copy(targetCRD1, crd1)
	io.Copy(targetCRD2, crd2)
	io.Copy(targetSHA, sha)

	type args struct {
		path string
	}
	type want struct {
		pkg        *ParsedPackage
		numObjects int
		err        error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Should return a ParsedPackage and no error.",
			args: args{
				path: path,
			},
			want: want{
				pkg: &ParsedPackage{
					SHA: testDigestFile,
					Deps: []v1beta1.Dependency{
						{
							Package:     "crossplane/provider-aws",
							Type:        v1beta1.ProviderPackageType,
							Constraints: "v0.20.0",
						},
					},
					MetaObj: &xpmetav1alpha1.Provider{
						TypeMeta: apimetav1.TypeMeta{
							APIVersion: "meta.pkg.crossplane.io/v1alpha1",
							Kind:       "Provider",
						},
						ObjectMeta: apimetav1.ObjectMeta{
							Name: "provider-helm",
						},
						Spec: xpmetav1alpha1.ProviderSpec{
							Controller: xpmetav1alpha1.ControllerSpec{
								Image: "crossplane/provider-helm-controller:v0.9.0",
							},
							MetaSpec: xpmetav1alpha1.MetaSpec{
								DependsOn: []xpmetav1alpha1.Dependency{
									{
										Provider: pointer.String("crossplane/provider-aws"),
										Version:  "v0.20.0",
									},
								},
							},
						},
					},
					PType: v1beta1.ProviderPackageType,
					Reg:   "index.docker.io/crossplane/provider-helm",
					Ver:   "v0.9.0",
				},
				numObjects: 2,
			},
		},
		"ErrInvalidPackagePath": {
			reason: "Should return an error if path is invalid.",
			args: args{
				path: "/notapackage",
			},
			want: want{
				err: errors.New(errInvalidPath),
			},
		},
		"ErrInvalidPackage": {
			reason: "Should return an error if package is invalid at provided path.",
			args: args{
				path: "/notapackage@v0.0.0",
			},
			want: want{
				err: &os.PathError{Op: "open", Path: "/notapackage@v0.0.0", Err: os.ErrNotExist},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parser, _ := parser.New()

			pkgres := NewResolver(
				WithParser(parser),
			)

			pkg, err := pkgres.FromDir(inmemfs, tc.args.path)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			if err == nil {

				if diff := cmp.Diff(tc.want.pkg.Digest(), pkg.Digest()); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Dependencies(), pkg.Dependencies()); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Meta(), pkg.Meta()); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.numObjects, len(pkg.Objects())); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Registry(), pkg.Registry()); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Type(), pkg.Type()); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.pkg.Version(), pkg.Version()); diff != "" {
					t.Errorf("\n%s\nFromDir(...): -want err, +got err:\n%s", tc.reason, diff)
				}
			}

		})
	}
}

func newPackageImage(path string) v1.Image {
	pack, _ := os.Open(path)

	info, _ := pack.Stat()

	buf := new(bytes.Buffer)

	tw := tar.NewWriter(buf)
	hdr := &tar.Header{
		Name: xpkg.StreamFile,
		Mode: int64(xpkg.StreamFileMode),
		Size: info.Size(),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = io.Copy(tw, pack)
	_ = tw.Close()
	packLayer, _ := tarball.LayerFromReader(buf)
	packImg, _ := mutate.AppendLayers(empty.Image, packLayer)

	return packImg
}

func digest(i v1.Image) string {
	h, _ := i.Digest()
	return h.String()
}