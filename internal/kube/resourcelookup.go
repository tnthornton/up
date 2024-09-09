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

package kube

import (
	"strings"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

var _ ResourceLookup = (*discoveryResourceLookup)(nil)

// ResourceLookup defines a struct that can be used to search a cluster for API
// resource metadata given a GVK.
type ResourceLookup interface {
	Get(gvk schema.GroupVersionKind) (metav1.APIResource, error)
}

// discoveryResourceLookup implements ResourceLookup, using the K8s discovery
// client for lookups, caching any queries in a map.
type discoveryResourceLookup struct {
	gvkToResourcesMap map[schema.GroupVersionKind]metav1.APIResource

	discovery *discovery.DiscoveryClient
}

// Get returns an API resource based on a GVK, searching for resources by group
// and version in the discovery client.
func (l *discoveryResourceLookup) Get(gvk schema.GroupVersionKind) (metav1.APIResource, error) {
	existing, ok := l.gvkToResourcesMap[gvk]
	if ok {
		return existing, nil
	}

	resources, err := l.discovery.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return metav1.APIResource{}, errors.Wrapf(err, "unable to find resources for gvk %q", gvk.String())
	}

	for _, resource := range resources.APIResources {
		// this API also returns subresources
		if strings.Contains(resource.Name, "/") {
			continue
		}

		newGVK := schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    resource.Kind,
		}
		// the api response doesn't populate GV
		resource.Group = newGVK.Group
		resource.Version = newGVK.Version
		l.gvkToResourcesMap[newGVK] = resource
	}

	existing, ok = l.gvkToResourcesMap[gvk]
	if ok {
		return existing, nil
	}
	return metav1.APIResource{}, errors.Errorf("gvk %q did not map to a resource in the cluster", gvk.String())
}

// NewDiscoveryResourceLookup creates a new discoveryResourceLookup using the
// given K8s REST config.
func NewDiscoveryResourceLookup(config *rest.Config) (*discoveryResourceLookup, error) {
	d, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	return &discoveryResourceLookup{
		discovery:         d,
		gvkToResourcesMap: map[schema.GroupVersionKind]metav1.APIResource{},
	}, nil
}
