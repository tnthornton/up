// create-test-package is a development utility command for creating test
// data. It pulls an xpkg from a registry, removes all the layers except the
// package layer, and saves it to a file. This lets us embed a variety of
// package images in our codebase as testdata without including a bunch of large
// layers that we don't need.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/upbound/up/internal/xpkg"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s <package:tag> <output file>\n", os.Args[0])
		os.Exit(1)
	}

	if err := realMain(os.Args[1], os.Args[2]); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func realMain(pkg, outfile string) error { // nolint:gocyclo
	pkgRef, err := name.ParseReference(pkg, name.WithDefaultRegistry("xpkg.upbound.io"))
	if err != nil {
		return errors.Wrap(err, "failed to parse package reference")
	}
	img, err := remote.Image(pkgRef)
	if err != nil {
		return errors.Wrap(err, "failed to create remote image")
	}

	pkgLayer, err := getPackageLayer(img)
	if err != nil {
		return err
	}

	newImg, err := mutate.AppendLayers(empty.Image, pkgLayer)
	if err != nil {
		return errors.Wrap(err, "failed to create test package image")
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return errors.Wrap(err, "failed to load cfg from image")
	}

	if cfg.Config.Labels == nil {
		cfg.Config.Labels = map[string]string{}
	}

	imgLayerDigest, err := newImg.Digest()
	if err != nil {
		return errors.Wrap(err, "failed to load digest from image")
	}

	imgLabelKey := xpkg.Label(imgLayerDigest.String())
	cfg.Config.Labels[imgLabelKey] = xpkg.PackageAnnotation

	newImg, err = mutate.Config(newImg, cfg.Config)
	if err != nil {
		return errors.Wrap(err, "failed to mutate image")
	}

	w, err := os.Create(outfile) //nolint:gosec // Intentional user-provided output file.
	if err != nil {
		return err
	}
	err = tarball.Write(nil, newImg, w)
	if err != nil {
		return errors.Wrap(err, "failed to write test package")
	}

	return nil
}

func getPackageLayer(img v1.Image) (v1.Layer, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image config file")
	}

	var pkgLayerDigest v1.Hash
	keyPrefix := xpkg.AnnotationKey + ":"
	for k, v := range cfgFile.Config.Labels {
		if v == xpkg.PackageAnnotation && strings.HasPrefix(k, keyPrefix) {
			k = strings.TrimPrefix(k, keyPrefix)
			pkgLayerDigest, err = v1.NewHash(k)
			if err != nil {
				return nil, errors.Wrapf(err, "package layer has invalid digest %q", v)
			}
			break
		}
	}

	pkgLayer, err := img.LayerByDigest(pkgLayerDigest)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get package layer")
	}

	return pkgLayer, nil
}
