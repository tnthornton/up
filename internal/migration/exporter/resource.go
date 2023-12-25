package exporter

import (
	"context"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ResourceExporter interface {
	ExportResources(ctx context.Context, gvr schema.GroupVersionResource) error
}

type UnstructuredExporter struct {
	fetcher   ResourceFetcher
	persister ResourcePersister
}

func NewUnstructuredExporter(f ResourceFetcher, p ResourcePersister) *UnstructuredExporter {
	return &UnstructuredExporter{
		fetcher:   f,
		persister: p,
	}
}

func (e *UnstructuredExporter) ExportResources(ctx context.Context, gvr schema.GroupVersionResource) error {
	resources, err := e.fetcher.FetchResources(ctx, gvr)
	if err != nil {
		return errors.Wrap(err, "cannot fetch resources")
	}

	if err = e.persister.PersistResources(ctx, gvr.GroupResource().String(), resources); err != nil {
		return errors.Wrap(err, "cannot persist resources")
	}

	return nil
}
