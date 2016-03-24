package migration

import(
	"fmt"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/apis/extensions/validation"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/registry/generic"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/validation/field"
)

// migrationStrategy implements verification logic for Migrations.
type migrationStrategy struct {
	runtime.ObjectTyper
	api.NameGenerator
}

// Strategy is the default logic that applies when creating and updating Migration objects.
var Strategy = migrationStrategy{api.Scheme, api.SimpleNameGenerator}

// NamespaceScoped returns true because all Migrations need to be within a namespace.
func (migrationStrategy) NamespaceScoped() bool {
	return true
}

// PrepareForCreate clears the status of a Migration before creation.
func (migrationStrategy) PrepareForCreate(obj runtime.Object) {
	migration := obj.(*extensions.Migration)
	// create cannot set status
	migration.Status = extensions.MigrationStatus{}
}

// PrepareForUpdate clears fields that are not allowed to be set by end users on update.
func (migrationStrategy) PrepareForUpdate(obj, old runtime.Object) {
	newMigration := obj.(*extensions.Migration)
	oldMigration := old.(*extensions.Migration)
	// Update is not allowed to set status
	newMigration.Status = oldMigration.Status
}

// Validate validates a new Migration.
func (migrationStrategy) Validate(ctx api.Context, obj runtime.Object) field.ErrorList {
	migration := obj.(*extensions.Migration)
	err := validation.ValidateMigration(migration)
	return err
}

// Canonicalize noramlizes the object after validation.
func (migrationStrategy) Canonicalize(obj runtime.Object) {
}

// AllowUnconditionalUpdate is the default update policy for Migration objects.
func (migrationStrategy) AllowUnconditionalUpdate() bool {
	return true
}

// AllowCreateOnUpdate is false for Migration; this means POST is needed to create one.
func (migrationStrategy) AllowCreateOnUpdate() bool {
	return false
}

// ValidateUpdate is the default update validation for an end user.
func (migrationStrategy) ValidateUpdate(ctx api.Context, obj, old runtime.Object) field.ErrorList {
	validationErrorList := validation.ValidateMigration(obj.(*extensions.Migration))
	updateErrorList := validation.ValidateMigrationUpdate(obj.(*extensions.Migration), old.(*extensions.Migration))
	return append(validationErrorList, updateErrorList...)
}

type migrationStatusStrategy struct {
	migrationStrategy
}

var StatusStrategy = migrationStatusStrategy{Strategy}

func (migrationStatusStrategy) PrepareForUpdate(obj, old runtime.Object) {
	newMigration := obj.(*extensions.Migration)
	oldMigration := obj.(*extensions.Migration)
	newMigration.Spec = oldMigration.Spec
}

func (migrationStatusStrategy) ValidateUpdate(ctx api.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateMigrationUpdateStatus(obj.(*extensions.Migration), old.(*extensions.Migration))
}

// MigrationToSelectableFields returns a field set that represents the object for matching purposes.
func MigrationToSelectableFields(migration *extensions.Migration) fields.Set {
	objectMetaFieldsSet := generic.ObjectMetaFieldsSet(migration.ObjectMeta, true)

	// TODO: Add specific fields set here if needed, then merge below with generic.MergeFieldSets({a}, {b})

	return objectMetaFieldsSet
}

// MatchMigration is the filter used by the generic etcd backend to route
// watch events from etcd to clients of the apiserver only interested in specific
// labels/fields.
func MatchMigration(label labels.Selector, field fields.Selector) generic.Matcher {
	return &generic.SelectionPredicate{
		Label: label,
		Field: field,
		GetAttrs: func(obj runtime.Object) (labels.Set, fields.Set, error) {
			migration, ok := obj.(*extensions.Migration)
			if !ok {
				return nil, nil, fmt.Errorf("Given object is not a migration.")
			}
			return labels.Set(migration.ObjectMeta.Labels), MigrationToSelectableFields(migration), nil
		},
	}
}
