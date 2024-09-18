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
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

// Image wraps a v1.Image and extends it with ImageMeta.
type Image struct {
	Meta  ImageMeta `json:"meta"`
	Image v1.Image
}

// ImageMeta contains metadata information about the Package Image.
type ImageMeta struct {
	Repo     string `json:"repo"`
	Registry string `json:"registry"`
	Version  string `json:"version"`
	Digest   string `json:"digest"`
}

// AnnotateImage reads in the layers of the given v1.Image and annotates the
// xpkg layers with their corresponding annotations, returning a new v1.Image
// containing the annotation details.
func AnnotateImage(i v1.Image) (v1.Image, error) { //nolint:gocyclo
	cfgFile, err := i.ConfigFile()
	if err != nil {
		return nil, err
	}

	layers, err := i.Layers()
	if err != nil {
		return nil, err
	}

	addendums := make([]mutate.Addendum, 0)

	for _, l := range layers {
		d, err := l.Digest()
		if err != nil {
			return nil, err
		}
		if annotation, ok := cfgFile.Config.Labels[Label(d.String())]; ok {
			addendums = append(addendums, mutate.Addendum{
				Layer: l,
				Annotations: map[string]string{
					AnnotationKey: annotation,
				},
			})
			continue
		}
		addendums = append(addendums, mutate.Addendum{
			Layer: l,
		})
	}

	// we didn't find any annotations, return original image
	if len(addendums) == 0 {
		return i, nil
	}

	img := empty.Image
	for _, a := range addendums {
		img, err = mutate.Append(img, a)
		if err != nil {
			return nil, errors.Wrap(err, "failed to build annotated image")
		}
	}

	return mutate.ConfigFile(img, cfgFile)
}

func BuildIndex(imgs ...v1.Image) (v1.ImageIndex, error) {
	adds := make([]mutate.IndexAddendum, 0, len(imgs))
	for _, img := range imgs {
		aimg, err := AnnotateImage(img)
		if err != nil {
			return nil, err
		}
		mt, err := aimg.MediaType()
		if err != nil {
			return nil, err
		}

		conf, err := aimg.ConfigFile()
		if err != nil {
			return nil, err
		}

		adds = append(adds, mutate.IndexAddendum{
			Add: aimg,
			Descriptor: v1.Descriptor{
				MediaType: mt,
				Platform: &v1.Platform{
					Architecture: conf.Architecture,
					OS:           conf.OS,
					OSVersion:    conf.OSVersion,
				},
			},
		})
	}

	return mutate.AppendManifests(empty.Index, adds...), nil
}
