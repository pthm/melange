package sqlgen

// =============================================================================
// List Objects Queries
// =============================================================================

type ListObjectsDirectInput struct {
	ObjectType          string
	Relations           []string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          ExclusionConfig
}

func ListObjectsDirectQuery(input ListObjectsDirectInput) (string, error) {
	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: input.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		SelectCol("object_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListObjectsUsersetSubjectInput struct {
	ObjectType    string
	Relations     []string
	ClosureValues string      // Deprecated: use ClosureRows
	ClosureRows   []ValuesRow // Typed closure rows (preferred)
	Exclusions    ExclusionConfig
}

func ListObjectsUsersetSubjectQuery(input ListObjectsUsersetSubjectInput) (string, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(input.ClosureRows, input.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: SubjectType},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	// Subject match: either exact match or userset object ID match with closure exists
	subjectMatch := Or(
		Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: SubjectID},
		And(
			Eq{
				Left:  UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				Right: UsersetObjectID{Source: SubjectID},
			},
			Raw(closureExistsStmt.Exists()),
		),
	)

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			HasUserset{Source: SubjectID},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			subjectMatch,
		).
		SelectCol("object_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListObjectsComplexClosureInput struct {
	ObjectType          string
	Relation            string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	Exclusions          ExclusionConfig
}

func ListObjectsComplexClosureQuery(input ListObjectsComplexClosureInput) (string, error) {
	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: input.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, input.AllowWildcard),
			CheckPermission{
				Subject:     SubjectParams(),
				Relation:    input.Relation,
				Object:      LiteralObject(input.ObjectType, Col{Table: "t", Column: "object_id"}),
				ExpectAllow: true,
			},
		).
		SelectCol("object_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

func ListObjectsIntersectionClosureQuery(functionName string) (string, error) {
	// Pass NULL for pagination params - inner function should return all results,
	// outer pagination wrapper handles limiting
	// Use alias to avoid column ambiguity with pagination-returning functions
	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
		FromExpr: FunctionCallExpr{
			Name:  functionName,
			Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
			Alias: "icr",
		},
	}
	return stmt.SQL(), nil
}

func ListObjectsIntersectionClosureValidatedQuery(objectType, relation, functionName string) (string, error) {
	// Pass NULL for pagination params - inner function should return all results,
	// outer pagination wrapper handles limiting
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
		FromExpr: FunctionCallExpr{
			Name:  functionName,
			Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
			Alias: "icr",
		},
		Where: CheckPermission{
			Subject:     SubjectParams(),
			Relation:    relation,
			Object:      LiteralObject(objectType, Col{Table: "icr", Column: "object_id"}),
			ExpectAllow: true,
		},
	}
	return stmt.SQL(), nil
}

type ListObjectsUsersetPatternSimpleInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	SatisfyingRelations []string
	AllowedSubjectTypes []string
	AllowWildcard       bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionConfig
}

func ListObjectsUsersetPatternSimpleQuery(input ListObjectsUsersetPatternSimpleInput) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{
			Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
			Right: Lit(input.SubjectRelation),
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject:     SubjectParams(),
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, Col{Table: "t", Column: "object_id"}),
			ExpectAllow: true,
		})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("m",
			Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(input.SubjectType)},
			Eq{
				Left:  Col{Table: "m", Column: "object_id"},
				Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			},
			In{Expr: Col{Table: "m", Column: "relation"}, Values: input.SatisfyingRelations},
			Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: input.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "m", Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		SelectCol("object_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListObjectsUsersetPatternComplexInput struct {
	ObjectType       string
	SubjectType      string
	SubjectRelation  string
	SourceRelations  []string
	IsClosurePattern bool
	SourceRelation   string
	Exclusions       ExclusionConfig
}

func ListObjectsUsersetPatternComplexQuery(input ListObjectsUsersetPatternComplexInput) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{
			Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
			Right: Lit(input.SubjectRelation),
		},
		CheckPermission{
			Subject:  SubjectParams(),
			Relation: input.SubjectRelation,
			Object: ObjectRef{
				Type: Lit(input.SubjectType),
				ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			},
			ExpectAllow: true,
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject:     SubjectParams(),
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, Col{Table: "t", Column: "object_id"}),
			ExpectAllow: true,
		})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		SelectCol("object_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListObjectsSelfCandidateInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string      // Deprecated: use ClosureRows
	ClosureRows   []ValuesRow // Typed closure rows (preferred)
}

func ListObjectsSelfCandidateQuery(input ListObjectsSelfCandidateInput) (string, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(input.ClosureRows, input.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{SelectAs(UsersetObjectID{Source: SubjectID}, "object_id")},
		Where: And(
			HasUserset{Source: SubjectID},
			Eq{Left: SubjectType, Right: Lit(input.ObjectType)},
			Raw(closureExistsStmt.Exists()),
		),
	}

	return stmt.SQL(), nil
}

type ListObjectsCrossTypeTTUInput struct {
	ObjectType      string
	LinkingRelation string
	Relation        string
	CrossTypes      []string
	Exclusions      ExclusionConfig
}

func ListObjectsCrossTypeTTUQuery(input ListObjectsCrossTypeTTUInput) (string, error) {
	q := Tuples("child").
		ObjectType(input.ObjectType).
		Relations(input.LinkingRelation).
		Where(
			In{Expr: Col{Table: "child", Column: "subject_type"}, Values: input.CrossTypes},
			CheckPermission{
				Subject:  SubjectParams(),
				Relation: input.Relation,
				Object: ObjectRef{
					Type: Col{Table: "child", Column: "subject_type"},
					ID:   Col{Table: "child", Column: "subject_id"},
				},
				ExpectAllow: true,
			},
		).
		SelectCol("object_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListObjectsRecursiveTTUInput struct {
	ObjectType       string
	LinkingRelations []string
	Exclusions       ExclusionConfig
}

func ListObjectsRecursiveTTUQuery(input ListObjectsRecursiveTTUInput) (string, error) {
	depthLimit := Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)}
	whereExprs := append([]Expr{depthLimit}, input.Exclusions.BuildPredicates()...)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth"},
		From:     "accessible",
		Alias:    "a",
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "child",
				On: And(
					Eq{Left: Col{Table: "child", Column: "object_type"}, Right: Lit(input.ObjectType)},
					In{Expr: Col{Table: "child", Column: "relation"}, Values: input.LinkingRelations},
					Eq{Left: Col{Table: "child", Column: "subject_type"}, Right: Lit(input.ObjectType)},
					Eq{Left: Col{Table: "child", Column: "subject_id"}, Right: Col{Table: "a", Column: "object_id"}},
				),
			},
		},
		Where: And(whereExprs...),
	}

	return stmt.SQL(), nil
}

// =============================================================================
// List Subjects Queries
// =============================================================================

type ListSubjectsUsersetFilterInput struct {
	ObjectType          string
	RelationList        []string
	AllowedSubjectTypes []string
	ObjectIDExpr        Expr
	FilterTypeExpr      Expr
	FilterRelationExpr  Expr
	ClosureValues       string      // Deprecated: use ClosureRows
	ClosureRows         []ValuesRow // Typed closure rows (preferred)
	UseTypeGuard        bool
	ExtraPredicatesSQL  []string // Raw SQL predicate strings
}

func ListSubjectsUsersetFilterQuery(input ListSubjectsUsersetFilterInput) (string, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(input.ClosureRows, input.ClosureValues, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: input.FilterTypeExpr},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: input.FilterRelationExpr},
		),
	}

	// Normalized subject expression: split_part(subject_id, '#', 1) || '#' || filter_relation
	normalizedSubject := SelectAs(NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, input.FilterRelationExpr), "subject_id")

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: input.ObjectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: input.FilterTypeExpr},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Or(
			Eq{Left: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: input.FilterRelationExpr},
			ExistsExpr(closureExistsStmt),
		),
	}

	if input.UseTypeGuard {
		conditions = append(conditions, In{Expr: input.FilterTypeExpr, Values: input.AllowedSubjectTypes})
	}

	for _, sql := range input.ExtraPredicatesSQL {
		conditions = append(conditions, Raw(sql))
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.RelationList...).
		Where(conditions...).
		SelectExpr(normalizedSubject).
		Distinct()

	return q.SQL(), nil
}

type ListSubjectsSelfCandidateInput struct {
	ObjectType         string
	Relation           string
	ObjectIDExpr       Expr
	FilterTypeExpr     Expr
	FilterRelationExpr Expr
	ClosureValues      string      // Deprecated: use ClosureRows
	ClosureRows        []ValuesRow // Typed closure rows (preferred)
	ExtraPredicatesSQL []string    // Raw SQL predicate strings
}

func ListSubjectsSelfCandidateQuery(input ListSubjectsSelfCandidateInput) (string, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(input.ClosureRows, input.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: input.FilterRelationExpr},
		),
	}

	conditions := make([]Expr, 0, 2+len(input.ExtraPredicatesSQL))
	conditions = append(conditions,
		Eq{Left: input.FilterTypeExpr, Right: Lit(input.ObjectType)},
		ExistsExpr(closureExistsStmt),
	)

	for _, sql := range input.ExtraPredicatesSQL {
		conditions = append(conditions, Raw(sql))
	}

	// Subject ID output: object_id || '#' || filter_relation
	subjectIDCol := SelectAs(Concat{Parts: []Expr{input.ObjectIDExpr, Lit("#"), input.FilterRelationExpr}}, "subject_id")

	stmt := SelectStmt{
		ColumnExprs: []Expr{subjectIDCol},
		Where:       And(conditions...),
	}

	return stmt.SQL(), nil
}

type ListSubjectsDirectInput struct {
	ObjectType      string
	RelationList    []string
	ObjectIDExpr    Expr
	SubjectTypeExpr Expr
	ExcludeWildcard bool
	Exclusions      ExclusionConfig
}

func ListSubjectsDirectQuery(input ListSubjectsDirectInput) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: input.ObjectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: input.SubjectTypeExpr},
	}

	if input.ExcludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.RelationList...).
		Where(conditions...).
		SelectCol("subject_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListSubjectsComplexClosureInput struct {
	ObjectType      string
	Relation        string
	ObjectIDExpr    Expr
	SubjectTypeExpr Expr
	ExcludeWildcard bool
	Exclusions      ExclusionConfig
}

func ListSubjectsComplexClosureQuery(input ListSubjectsComplexClosureInput) (string, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: input.ObjectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: input.SubjectTypeExpr},
		CheckPermission{
			Subject: SubjectRef{
				Type: input.SubjectTypeExpr,
				ID:   Col{Table: "t", Column: "subject_id"},
			},
			Relation:    input.Relation,
			Object:      LiteralObject(input.ObjectType, input.ObjectIDExpr),
			ExpectAllow: true,
		},
	}

	if input.ExcludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(conditions...).
		SelectCol("subject_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

func ListSubjectsIntersectionClosureQuery(functionName string, subjectTypeExpr Expr) (string, error) {
	// Inner list functions don't have pagination params - they return all results.
	// Outer pagination wrapper handles limiting.
	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "ics", Column: "subject_id"}},
		FromExpr: FunctionCallExpr{
			Name:  functionName,
			Args:  []Expr{ObjectID, subjectTypeExpr},
			Alias: "ics",
		},
	}
	return stmt.SQL(), nil
}

func ListSubjectsIntersectionClosureValidatedQuery(objectType, relation, functionName string, functionSubjectTypeExpr, checkSubjectTypeExpr, objectIDExpr Expr) (string, error) {
	// Inner list functions don't have pagination params - they return all results.
	// Outer pagination wrapper handles limiting.
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "ics", Column: "subject_id"}},
		FromExpr: FunctionCallExpr{
			Name:  functionName,
			Args:  []Expr{objectIDExpr, functionSubjectTypeExpr},
			Alias: "ics",
		},
		Where: CheckPermissionCall{
			FunctionName: "check_permission",
			Subject: SubjectRef{
				Type: checkSubjectTypeExpr,
				ID:   Col{Table: "ics", Column: "subject_id"},
			},
			Relation:    relation,
			Object:      LiteralObject(objectType, objectIDExpr),
			ExpectAllow: true,
		},
	}
	return stmt.SQL(), nil
}

type ListSubjectsUsersetPatternSimpleInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	SatisfyingRelations []string
	ObjectIDExpr        Expr
	SubjectTypeExpr     Expr
	AllowedSubjectTypes []string
	ExcludeWildcard     bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionConfig
}

func ListSubjectsUsersetPatternSimpleQuery(input ListSubjectsUsersetPatternSimpleInput) (string, error) {
	// Join conditions for the userset membership table
	joinConditions := []Expr{
		Eq{Left: Col{Table: "s", Column: "object_type"}, Right: Lit(input.SubjectType)},
		Eq{Left: Col{Table: "s", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		In{Expr: Col{Table: "s", Column: "relation"}, Values: input.SatisfyingRelations},
		Eq{Left: Col{Table: "s", Column: "subject_type"}, Right: input.SubjectTypeExpr},
		In{Expr: input.SubjectTypeExpr, Values: input.AllowedSubjectTypes},
	}

	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, Ne{Left: Col{Table: "s", Column: "subject_id"}, Right: Lit("*")})
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: input.ObjectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(input.SubjectRelation)},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject: SubjectRef{
				Type: input.SubjectTypeExpr,
				ID:   Col{Table: "s", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, input.ObjectIDExpr),
			ExpectAllow: true,
		})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("s", joinConditions...).
		Select("s.subject_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}

type ListSubjectsUsersetPatternComplexInput struct {
	ObjectType       string
	SubjectType      string
	SubjectRelation  string
	SourceRelations  []string
	ObjectIDExpr     Expr
	SubjectTypeExpr  Expr
	IsClosurePattern bool
	SourceRelation   string
	Exclusions       ExclusionConfig
}

func ListSubjectsUsersetPatternComplexQuery(input ListSubjectsUsersetPatternComplexInput) (string, error) {
	whereExprs := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(input.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: input.SourceRelations},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: input.ObjectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(input.SubjectRelation)},
	}

	if input.IsClosurePattern {
		whereExprs = append(whereExprs, CheckPermission{
			Subject: SubjectRef{
				Type: input.SubjectTypeExpr,
				ID:   Col{Table: "s", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, input.ObjectIDExpr),
			ExpectAllow: true,
		})
	}

	whereExprs = append(whereExprs, input.Exclusions.BuildPredicates()...)

	listFuncName := "list_" + Ident(input.SubjectType) + "_" + Ident(input.SubjectRelation) + "_subjects"
	listFunc := LateralFunction{
		Name:  listFuncName,
		Args:  []Expr{UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, input.SubjectTypeExpr},
		Alias: "s",
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{
			{
				Type:      "CROSS",
				TableExpr: listFunc,
			},
		},
		Where: And(whereExprs...),
	}

	return stmt.SQL(), nil
}

type ListSubjectsUsersetPatternRecursiveComplexInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	ObjectIDExpr        Expr
	SubjectTypeExpr     Expr
	AllowedSubjectTypes []string
	ExcludeWildcard     bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionConfig
}

func ListSubjectsUsersetPatternRecursiveComplexQuery(input ListSubjectsUsersetPatternRecursiveComplexInput) (string, error) {
	// Join conditions for the membership table
	joinConditions := []Expr{
		Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(input.SubjectType)},
		Eq{Left: Col{Table: "m", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: input.SubjectTypeExpr},
		In{Expr: input.SubjectTypeExpr, Values: input.AllowedSubjectTypes},
	}

	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, Ne{Left: Col{Table: "m", Column: "subject_id"}, Right: Lit("*")})
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: input.ObjectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(input.SubjectRelation)},
		CheckPermission{
			Subject: SubjectRef{
				Type: input.SubjectTypeExpr,
				ID:   Col{Table: "m", Column: "subject_id"},
			},
			Relation: input.SubjectRelation,
			Object: ObjectRef{
				Type: Lit(input.SubjectType),
				ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			},
			ExpectAllow: true,
		},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject: SubjectRef{
				Type: input.SubjectTypeExpr,
				ID:   Col{Table: "m", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, input.ObjectIDExpr),
			ExpectAllow: true,
		})
	}

	q := Tuples("t").
		ObjectType(input.ObjectType).
		Relations(input.SourceRelations...).
		Where(conditions...).
		JoinTuples("m", joinConditions...).
		Select("m.subject_id").
		Distinct()

	q.Where(input.Exclusions.BuildPredicates()...)

	return q.SQL(), nil
}
