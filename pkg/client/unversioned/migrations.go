package unversioned

import (
  "k8s.io/kubernetes/pkg/api"
  "k8s.io/kubernetes/pkg/apis/extensions"
  "k8s.io/kubernetes/pkg/watch"
)

// MigrationsNamespacer has methods to work with Migration resources in a namespace
type MigrationsNamespacer interface {
  Migrations(namespace string) MigrationInterface
}

// MigrationInterface exposes methods to work on Migration resources.
type MigrationInterface interface {
  List(opts api.ListOptions) (*extensions.MigrationList, error)
  Get(name string) (*extensions.Migration, error)
  Create(migration *extensions.Migration) (*extensions.Migration, error)
  Update(migration *extensions.Migration) (*extensions.Migration, error)
  Delete(name string, options *api.DeleteOptions) error
  Watch(opts api.ListOptions) (watch.Interface, error)
  UpdateStatus(migration *extensions.Migration) (*extensions.Migration, error)
}

// migrations implements MigrationsNamespacer interface
type migrations struct {
  r *ExtensionsClient
  ns string
}

// newMigrations returns a migrations
func newMigrations(c *ExtensionsClient, namespace string) *migrations {
  return &migrations{c, namespace}
}

// Ensure statically that migrations implements MigrationInterface.
var _ MigrationInterface = &migrations{}

// List returns a list of migrations that match the label and field selectors.
func (c *migrations) List(opts api.ListOptions) (result *extensions.MigrationList, err error) {
  result = &extensions.MigrationList{}
  err = c.r.Get().Namespace(c.ns).Resource("migrations").VersionedParams(&opts, api.ParameterCodec).Do().Into(result)
  return
}

// Get returns information about a particular migration.
func (c *migrations) Get(name string) (result *extensions.Migration, err error) {
  result = &extensions.Migration{}
  err = c.r.Get().Namespace(c.ns).Resource("migrations").Name(name).Do().Into(result)
  return
}

func (c *migrations) Create(migration *extensions.Migration) (result *extensions.Migration, err error) {
  result = &extensions.Migration{}
  err = c.r.Post().Namespace(c.ns).Resource("migrations").Body(migration).Do().Into(result)
  return
}

func (c *migrations) Update(migration *extensions.Migration) (result *extensions.Migration, err error) {
  result = &extensions.Migration{}
  err = c.r.Put().Namespace(c.ns).Resource("migrations").Name(migration.Name).Body(migration).Do().Into(result)
  return
}

func (c *migrations) Delete(name string, options *api.DeleteOptions) (err error) {
  return c.r.Delete().Namespace(c.ns).Resource("migrations").Name(name).Body(options).Do().Error()
}

func (c *migrations) Watch(opts api.ListOptions) (watch.Interface, error) {
  return c.r.Get().
    Prefix("watch").
    Namespace(c.ns).
    Resource("migrations").
    VersionedParams(&opts, api.ParameterCodec).
    Watch()
}

func (c *migrations) UpdateStatus(migration *extensions.Migration) (result *extensions.Migration, err error) {
  result = &extensions.Migration{}
  err = c.r.Put().Namespace(c.ns).Resource("migrations").Name(migration.Name).SubResource("status").Body(migration).Do().Into(result)
  return
}
