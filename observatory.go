package main

import (
	"A3S/internal/httpapi"
	"A3S/internal/observatory"
	"context"
)

// Local stores are queried via their existing service boundary. No directory
// names, tokens, buckets or object keys are exposed to the dashboard.
func localObservatory(stores []httpapi.Operations) observatory.Provider {
	return observatory.ProviderFunc(func(ctx context.Context, n observatory.Node) (observatory.Report, error) {
		r := observatory.Report{Nodes: []observatory.Node{n}, Database: observatory.Dependency{State: "not_applicable"}, ObjectStore: observatory.Dependency{State: "healthy"}, Storage: observatory.Storage{Available: true}}
		for _, s := range stores {
			u, err := s.Usage(ctx)
			if err != nil {
				r.Storage.Available = false
				r.ObjectStore.State = "unavailable"
				r.Nodes[0].State = "degraded"
				r.Warning = "local_storage_unavailable"
				return r, nil
			}
			r.Storage.Bytes += u.StoredBytes
			r.Storage.Objects += u.Objects
			r.Storage.Buckets += int64(u.Buckets)
		}
		return r, nil
	})
}
