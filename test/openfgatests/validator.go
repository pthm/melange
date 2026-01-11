package openfgatests

import (
	"fmt"
	"strings"

	"github.com/pthm/melange"
	"github.com/pthm/melange/tooling/schema"
)

type modelValidator struct {
	types     map[string]schema.TypeDefinition
	relations map[string]map[string]schema.RelationDefinition
}

func newModelValidator(types []schema.TypeDefinition) *modelValidator {
	typesByName := make(map[string]schema.TypeDefinition, len(types))
	relationsByType := make(map[string]map[string]schema.RelationDefinition, len(types))

	for _, t := range types {
		typesByName[t.Name] = t
		if len(t.Relations) == 0 {
			continue
		}
		relMap := make(map[string]schema.RelationDefinition, len(t.Relations))
		for _, rel := range t.Relations {
			relMap[rel.Name] = rel
		}
		relationsByType[t.Name] = relMap
	}

	return &modelValidator{
		types:     typesByName,
		relations: relationsByType,
	}
}

func validatorForStore(store *store, modelID string) (*modelValidator, error) {
	if store == nil {
		return nil, fmt.Errorf("store not found")
	}
	model, ok := store.models[modelID]
	if !ok {
		return nil, fmt.Errorf("model not found: %s", modelID)
	}
	return newModelValidator(model.types), nil
}

func (v *modelValidator) typeExists(name string) bool {
	_, ok := v.types[name]
	return ok
}

func (v *modelValidator) relationExists(objectType, relation string) bool {
	relations, ok := v.relations[objectType]
	if !ok {
		return false
	}
	_, ok = relations[relation]
	return ok
}

func (v *modelValidator) relationDef(objectType, relation string) (schema.RelationDefinition, bool) {
	relations, ok := v.relations[objectType]
	if !ok {
		return schema.RelationDefinition{}, false
	}
	rel, ok := relations[relation]
	return rel, ok
}

func (v *modelValidator) ValidateUsersetSubject(subject melange.Object) error {
	id := subject.ID
	idx := strings.Index(id, "#")
	if idx == -1 {
		return nil
	}

	rel := id[idx+1:]
	if rel == "" {
		return fmt.Errorf("userset subject relation missing for %s", subject.String())
	}

	if !v.typeExists(string(subject.Type)) {
		return fmt.Errorf("userset subject type %s not defined in model", subject.Type)
	}
	if !v.relationExists(string(subject.Type), rel) {
		return fmt.Errorf("userset subject relation %s not defined on %s", rel, subject.Type)
	}

	return nil
}

func (v *modelValidator) ValidateCheckRequest(subject melange.Object, relation melange.Relation, object melange.Object) error {
	if !v.typeExists(string(object.Type)) {
		return &melange.ValidationError{
			Code:    melange.ErrorCodeValidation,
			Message: fmt.Sprintf("type '%s' not found", object.Type),
		}
	}
	if !v.relationExists(string(object.Type), string(relation)) {
		return &melange.ValidationError{
			Code:    melange.ErrorCodeValidation,
			Message: fmt.Sprintf("relation '%s' is not a relation on type '%s'", relation, object.Type),
		}
	}
	if !v.typeExists(string(subject.Type)) {
		return &melange.ValidationError{
			Code:    melange.ErrorCodeValidation,
			Message: fmt.Sprintf("type '%s' not found", subject.Type),
		}
	}

	return nil
}

func (v *modelValidator) ValidateListUsersRequest(relation melange.Relation, object melange.Object, subjectType melange.ObjectType) error {
	if !v.typeExists(string(object.Type)) {
		return &melange.ValidationError{
			Code:    melange.ErrorCodeValidation,
			Message: fmt.Sprintf("type '%s' not found", object.Type),
		}
	}
	if !v.relationExists(string(object.Type), string(relation)) {
		return &melange.ValidationError{
			Code:    melange.ErrorCodeValidation,
			Message: fmt.Sprintf("relation '%s' is not a relation on type '%s'", relation, object.Type),
		}
	}

	filter := string(subjectType)
	if idx := strings.Index(filter, "#"); idx != -1 {
		baseType := filter[:idx]
		rel := filter[idx+1:]
		if !v.typeExists(baseType) {
			return &melange.ValidationError{
				Code:    melange.ErrorCodeValidation,
				Message: fmt.Sprintf("type '%s' not found", baseType),
			}
		}
		if !v.relationExists(baseType, rel) {
			return &melange.ValidationError{
				Code:    melange.ErrorCodeValidation,
				Message: fmt.Sprintf("relation '%s' is not a relation on type '%s'", rel, baseType),
			}
		}
	} else if !v.typeExists(filter) {
		return &melange.ValidationError{
			Code:    melange.ErrorCodeValidation,
			Message: fmt.Sprintf("type '%s' not found", filter),
		}
	}

	return nil
}

func (v *modelValidator) ValidateContextualTuple(tuple melange.ContextualTuple) error {
	relDef, ok := v.relationDef(string(tuple.Object.Type), string(tuple.Relation))
	if !ok {
		return fmt.Errorf("relation %s not defined on %s", tuple.Relation, tuple.Object.Type)
	}
	if !v.typeExists(string(tuple.Subject.Type)) {
		return fmt.Errorf("type '%s' not found", tuple.Subject.Type)
	}

	subjectID := tuple.Subject.ID
	idx := strings.Index(subjectID, "#")
	if idx != -1 {
		if idx == len(subjectID)-1 {
			return fmt.Errorf("userset subject relation missing for %s", tuple.Subject.String())
		}
		if err := v.ValidateUsersetSubject(tuple.Subject); err != nil {
			return err
		}
	}

	if !subjectAllowed(relDef, string(tuple.Subject.Type), subjectID) {
		return fmt.Errorf("subject type %s not allowed for %s on %s", tuple.Subject.Type, tuple.Relation, tuple.Object.Type)
	}

	return nil
}

func subjectAllowed(rel schema.RelationDefinition, subjectType, subjectID string) bool {
	isWildcard := subjectID == "*"
	idx := strings.Index(subjectID, "#")
	isUserset := idx != -1
	usersetRel := ""
	if isUserset {
		usersetRel = subjectID[idx+1:]
	}

	for _, ref := range rel.SubjectTypeRefs {
		if ref.Type != subjectType {
			continue
		}
		if isUserset {
			if ref.Relation != usersetRel {
				continue
			}
		} else if ref.Relation != "" {
			continue
		}
		if isWildcard && !ref.Wildcard {
			continue
		}
		return true
	}

	return false
}
