package sqlgen

import "fmt"

// ComposedObjectsBlockSet contains blocks for a composed list_objects function.
// Composed functions handle indirect anchor patterns (TTU and userset composition).
type ComposedObjectsBlockSet struct {
	// SelfBlock is the self-candidate check block
	SelfBlock *TypedQueryBlock

	// MainBlocks are the composed query blocks (TTU and/or userset paths)
	MainBlocks []TypedQueryBlock

	// AllowedSubjectTypes for the type guard
	AllowedSubjectTypes []string

	// Anchor metadata for comments
	AnchorType     string
	AnchorRelation string
	FirstStepType  string
}

// ComposedSubjectsBlockSet contains blocks for a composed list_subjects function.
type ComposedSubjectsBlockSet struct {
	// SelfBlock is the self-candidate check block (for userset filter)
	SelfBlock *TypedQueryBlock

	// UsersetFilterBlocks are candidate blocks for userset filter path
	UsersetFilterBlocks []TypedQueryBlock

	// RegularBlocks are candidate blocks for regular path
	RegularBlocks []TypedQueryBlock

	// AllowedSubjectTypes for the type guard
	AllowedSubjectTypes []string

	// HasExclusions indicates if exclusion predicates are needed
	HasExclusions bool

	// Anchor metadata for comments
	AnchorType     string
	AnchorRelation string
	FirstStepType  string
}

// BuildListObjectsComposedBlocks builds block set for composed list_objects function.
func BuildListObjectsComposedBlocks(plan ListPlan) (ComposedObjectsBlockSet, error) {
	anchor := plan.Analysis.IndirectAnchor
	if anchor == nil || len(anchor.Path) == 0 {
		return ComposedObjectsBlockSet{}, fmt.Errorf("missing indirect anchor data for %s.%s", plan.ObjectType, plan.Relation)
	}

	var result ComposedObjectsBlockSet
	result.AllowedSubjectTypes = plan.AllowedSubjectTypes
	result.AnchorType = anchor.AnchorType
	result.AnchorRelation = anchor.AnchorRelation
	result.FirstStepType = anchor.Path[0].Type

	// Build self-candidate block
	selfBlock, err := buildComposedObjectsSelfBlock(plan)
	if err != nil {
		return ComposedObjectsBlockSet{}, err
	}
	result.SelfBlock = selfBlock

	// Build main composed query blocks
	firstStep := anchor.Path[0]
	exclusions := buildSimpleComplexExclusionInput(plan.Analysis, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)

	switch firstStep.Type {
	case "ttu":
		// Build TTU blocks for each target type
		for _, targetType := range firstStep.AllTargetTypes {
			block, err := buildComposedTTUObjectsBlock(plan, anchor, targetType, exclusions)
			if err != nil {
				return ComposedObjectsBlockSet{}, err
			}
			result.MainBlocks = append(result.MainBlocks, *block)
		}

		// Build recursive TTU blocks
		for _, recursiveType := range firstStep.RecursiveTypes {
			block, err := buildComposedRecursiveTTUObjectsBlock(plan, anchor, recursiveType, exclusions)
			if err != nil {
				return ComposedObjectsBlockSet{}, err
			}
			result.MainBlocks = append(result.MainBlocks, *block)
		}

	case "userset":
		block, err := buildComposedUsersetObjectsBlock(plan, firstStep, exclusions)
		if err != nil {
			return ComposedObjectsBlockSet{}, err
		}
		result.MainBlocks = append(result.MainBlocks, *block)
	}

	return result, nil
}

// buildComposedObjectsSelfBlock builds the self-candidate check block.
func buildComposedObjectsSelfBlock(plan ListPlan) (*TypedQueryBlock, error) {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{SelectAs(UsersetObjectID{Source: SubjectID}, "object_id")},
		Where: And(
			Eq{Left: SubjectType, Right: Lit(plan.ObjectType)},
			HasUserset{Source: SubjectID},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: when subject is a userset on the same object type",
		},
		Query: stmt,
	}, nil
}

// buildComposedTTUObjectsBlock builds a TTU composition block.
func buildComposedTTUObjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, targetType string, exclusions ExclusionConfig) (*TypedQueryBlock, error) {
	exclusionPreds := exclusions.BuildPredicates()

	// Build subquery for list function call using typed DSL
	inSubquery := InFunctionSelect{
		Expr:      Col{Table: "t", Column: "subject_id"},
		FuncName:  ListObjectsFunctionName(targetType, anchor.Path[0].TargetRelation),
		Args:      []Expr{SubjectType, SubjectID, Null{}, Null{}},
		Alias:     "obj",
		SelectCol: "object_id",
	}

	conditions := make([]Expr, 0, 4+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(targetType)},
		inSubquery,
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- TTU composition: %s -> %s", anchor.Path[0].LinkingRelation, targetType),
		},
		Query: stmt,
	}, nil
}

// buildComposedRecursiveTTUObjectsBlock builds a recursive TTU composition block.
func buildComposedRecursiveTTUObjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, recursiveType string, exclusions ExclusionConfig) (*TypedQueryBlock, error) {
	exclusionPreds := exclusions.BuildPredicates()

	conditions := make([]Expr, 0, 4+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(recursiveType)},
		CheckPermissionInternalExpr(SubjectParams(), anchor.Path[0].TargetRelation, ObjectRef{Type: Lit(recursiveType), ID: Col{Table: "t", Column: "subject_id"}}, true),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Recursive TTU: %s -> %s", anchor.Path[0].LinkingRelation, recursiveType),
		},
		Query: stmt,
	}, nil
}

// buildComposedUsersetObjectsBlock builds a userset composition block.
func buildComposedUsersetObjectsBlock(plan ListPlan, firstStep AnchorPathStep, exclusions ExclusionConfig) (*TypedQueryBlock, error) {
	exclusionPreds := exclusions.BuildPredicates()

	// Build subquery for list function call using typed DSL
	// split_part(t.subject_id, '#', 1) extracts the object_id from the userset
	usersetObjectID := Raw("split_part(t.subject_id, '#', 1)")
	inSubquery := InFunctionSelect{
		Expr:      usersetObjectID,
		FuncName:  ListObjectsFunctionName(firstStep.SubjectType, firstStep.SubjectRelation),
		Args:      []Expr{SubjectType, SubjectID, Null{}, Null{}},
		Alias:     "obj",
		SelectCol: "object_id",
	}

	conditions := make([]Expr, 0, 6+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(firstStep.SubjectRelation)},
		Or(
			inSubquery,
			CheckPermissionInternalExpr(SubjectParams(), firstStep.SubjectRelation, ObjectRef{Type: Lit(firstStep.SubjectType), ID: usersetObjectID}, true),
		),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset composition: %s#%s", firstStep.SubjectType, firstStep.SubjectRelation),
		},
		Query: stmt,
	}, nil
}

// BuildListSubjectsComposedBlocks builds block set for composed list_subjects function.
