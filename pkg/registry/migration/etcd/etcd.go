package etcd

import (
  "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/registry/cachesize"
	"k8s.io/kubernetes/pkg/registry/generic"
	etcdgeneric "k8s.io/kubernetes/pkg/registry/generic/etcd"
	"k8s.io/kubernetes/pkg/registry/migration"
	"k8s.io/kubernetes/pkg/runtime"
)

// rest implements a RESTStorage for Migrations against etcd
type REST struct {
  *etcdgeneric.Etcd
}

func NewREST(opts generic.RESTOptions) (*REST, *StatusREST) {
  prefix := "/migrations"

  newListFunc := func() runtime.Object { return &extensions.MigrationList{} }
  storageInterface := opts.Decorator(
    opts.Storage, cachesize.GetWatchCacheSizeByResource(cachesize.Migrations), &extensions.Migration{}, prefix, migration.Strategy, newListFunc)

  store := &etcdgeneric.Etcd{
    NewFunc: func() runtime.Object { return &extensions.Migration{} },

    // NewListFunc returns an object capable of storing results of an etcd list.
    NewListFunc: newListFunc,
    // Produces a path that etcd understands, to the root of the resource
    // by combining the namespace in the context with the given prefix
    KeyRootFunc: func(ctx api.Context) string {
      return etcdgeneric.NamespaceKeyRootFunc(ctx, prefix)
    },
    // Produces a path that etcd understands, to the resource by combining
    // the namespace in the context with the given prefix
    KeyFunc: func(ctx api.Context, name string) (string, error) {
      return etcdgeneric.NamespaceKeyFunc(ctx, prefix, name)
    },
    // Retrieve the name field of a migration
    ObjectNameFunc: func(obj runtime.Object) (string, error) {
      return obj.(*extensions.Migration).Name, nil
    },
    // Used to match objects based on labels/fields for list and watch
    PredicateFunc: func(label labels.Selector, field fields.Selector) generic.Matcher {
      return migration.MatchMigration(label, field)
    },
    QualifiedResource:       extensions.Resource("migrations"),
    DeleteCollectionWorkers: opts.DeleteCollectionWorkers,

    // Used to validate job creation
    CreateStrategy: migration.Strategy,

    // Used to validate job updates
    UpdateStrategy: migration.Strategy,

    Storage: storageInterface,
  }

  statusStore := *store
  statusStore.UpdateStrategy = migration.StatusStrategy
  return &REST{store}, &StatusREST{store: &statusStore}
}

// StatusREST implements the REST endpoint for changing the status of a resourcequota.
type StatusREST struct {
  store *etcdgeneric.Etcd
}

func (r *StatusREST) New() runtime.Object {
  return &extensions.Migration{}
}

// Update alters the status subset of an object.
func (r *StatusREST) Update(ctx api.Context, obj runtime.Object) (runtime.Object, bool, error) {
  return r.store.Update(ctx, obj)
}
