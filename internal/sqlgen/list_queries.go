package sqlgen

// =============================================================================
// Helper Functions
// =============================================================================

// stringToDSLExpr converts a string expression to Expr.
// Recognizes common parameter names and converts them to DSL constants.
func stringToDSLExpr(s string) Expr {
	if s == "" {
		return nil
	}
	switch s {
	case "p_subject_type":
		return SubjectType
	case "p_subject_id":
		return SubjectID
	case "p_object_type":
		return ObjectType
	case "p_object_id":
		return ObjectID
	default:
		return Raw(s)
	}
}

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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsUsersetSubjectInput struct {
	ObjectType    string
	Relations     []string
	ClosureValues string
	Exclusions    ExclusionConfig
}

func ListObjectsUsersetSubjectQuery(input ListObjectsUsersetSubjectInput) (string, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureValuesTable(input.ClosureValues, "c"),
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

func ListObjectsIntersectionClosureQuery(functionName string) (string, error) {
	stmt := SelectStmt{
		Columns: []string{"*"},
		From:    functionName + "(p_subject_type, p_subject_id)",
	}
	return stmt.SQL(), nil
}

func ListObjectsIntersectionClosureValidatedQuery(objectType, relation, functionName string) (string, error) {
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"icr.object_id"},
		From:     functionName + "(p_subject_type, p_subject_id)",
		Alias:    "icr",
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsSelfCandidateInput struct {
	ObjectType    string
	Relation      string
	ClosureValues string
}

func ListObjectsSelfCandidateQuery(input ListObjectsSelfCandidateInput) (string, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureValuesTable(input.ClosureValues, "c"),
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListObjectsRecursiveTTUInput struct {
	ObjectType       string
	LinkingRelations []string
	Exclusions       ExclusionConfig
}

func ListObjectsRecursiveTTUQuery(input ListObjectsRecursiveTTUInput) (string, error) {
	// This is a CTE recursive query pattern - uses 'accessible' as the source table
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
		Where: Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)},
	}

	// Add exclusion predicates to WHERE
	predicates := input.Exclusions.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]Expr{stmt.Where}, predicates...)
		stmt.Where = And(allPredicates...)
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
	ObjectIDExpr        string
	FilterTypeExpr      string
	FilterRelationExpr  string
	ClosureValues       string
	UseTypeGuard        bool
	ExtraPredicatesSQL  []string // Raw SQL predicate strings
}

func ListSubjectsUsersetFilterQuery(input ListSubjectsUsersetFilterInput) (string, error) {
	filterTypeExpr := stringToDSLExpr(input.FilterTypeExpr)
	filterRelationExpr := stringToDSLExpr(input.FilterRelationExpr)
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)

	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureValuesTable(input.ClosureValues, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: filterTypeExpr},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: filterRelationExpr},
		),
	}

	// Normalized subject expression: split_part(subject_id, '#', 1) || '#' || filter_relation
	normalizedSubject := SelectAs(NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, filterRelationExpr), "subject_id")

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: filterTypeExpr},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Or(
			Eq{Left: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: filterRelationExpr},
			ExistsExpr(closureExistsStmt),
		),
	}

	if input.UseTypeGuard {
		conditions = append(conditions, In{Expr: filterTypeExpr, Values: input.AllowedSubjectTypes})
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
	ObjectIDExpr       string
	FilterTypeExpr     string
	FilterRelationExpr string
	ClosureValues      string
	ExtraPredicatesSQL []string // Raw SQL predicate strings
}

func ListSubjectsSelfCandidateQuery(input ListSubjectsSelfCandidateInput) (string, error) {
	filterTypeExpr := stringToDSLExpr(input.FilterTypeExpr)
	filterRelationExpr := stringToDSLExpr(input.FilterRelationExpr)
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)

	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureValuesTable(input.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(input.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(input.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: filterRelationExpr},
		),
	}

	conditions := make([]Expr, 0, 2+len(input.ExtraPredicatesSQL))
	conditions = append(conditions,
		Eq{Left: filterTypeExpr, Right: Lit(input.ObjectType)},
		ExistsExpr(closureExistsStmt),
	)

	for _, sql := range input.ExtraPredicatesSQL {
		conditions = append(conditions, Raw(sql))
	}

	// Subject ID output: object_id || '#' || filter_relation
	subjectIDCol := SelectAs(Concat{Parts: []Expr{objectIDExpr, Lit("#"), filterRelationExpr}}, "subject_id")

	stmt := SelectStmt{
		ColumnExprs: []Expr{subjectIDCol},
		Where:       And(conditions...),
	}

	return stmt.SQL(), nil
}

type ListSubjectsDirectInput struct {
	ObjectType      string
	RelationList    []string
	ObjectIDExpr    string
	SubjectTypeExpr string
	ExcludeWildcard bool
	Exclusions      ExclusionConfig
}

func ListSubjectsDirectQuery(input ListSubjectsDirectInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: subjectTypeExpr},
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListSubjectsComplexClosureInput struct {
	ObjectType      string
	Relation        string
	ObjectIDExpr    string
	SubjectTypeExpr string
	ExcludeWildcard bool
	Exclusions      ExclusionConfig
}

func ListSubjectsComplexClosureQuery(input ListSubjectsComplexClosureInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: subjectTypeExpr},
		CheckPermission{
			Subject: SubjectRef{
				Type: subjectTypeExpr,
				ID:   Col{Table: "t", Column: "subject_id"},
			},
			Relation:    input.Relation,
			Object:      LiteralObject(input.ObjectType, objectIDExpr),
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

func ListSubjectsIntersectionClosureQuery(functionName, subjectTypeExpr string) (string, error) {
	stmt := SelectStmt{
		Columns: []string{"*"},
		From:    functionName + "(p_object_id, " + subjectTypeExpr + ")",
	}
	return stmt.SQL(), nil
}

func ListSubjectsIntersectionClosureValidatedQuery(objectType, relation, functionName, functionSubjectTypeExpr, checkSubjectTypeExpr, objectIDExpr string) (string, error) {
	checkSubjectType := stringToDSLExpr(checkSubjectTypeExpr)
	objectID := stringToDSLExpr(objectIDExpr)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"ics.subject_id"},
		From:     functionName + "(" + objectIDExpr + ", " + functionSubjectTypeExpr + ")",
		Alias:    "ics",
		Where: CheckPermissionCall{
			FunctionName: "check_permission",
			Subject: SubjectRef{
				Type: checkSubjectType,
				ID:   Col{Table: "ics", Column: "subject_id"},
			},
			Relation:    relation,
			Object:      LiteralObject(objectType, objectID),
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
	ObjectIDExpr        string
	SubjectTypeExpr     string
	AllowedSubjectTypes []string
	ExcludeWildcard     bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionConfig
}

func ListSubjectsUsersetPatternSimpleQuery(input ListSubjectsUsersetPatternSimpleInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	// Join conditions for the userset membership table
	joinConditions := []Expr{
		Eq{Left: Col{Table: "s", Column: "object_type"}, Right: Lit(input.SubjectType)},
		Eq{Left: Col{Table: "s", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		In{Expr: Col{Table: "s", Column: "relation"}, Values: input.SatisfyingRelations},
		Eq{Left: Col{Table: "s", Column: "subject_type"}, Right: subjectTypeExpr},
		In{Expr: subjectTypeExpr, Values: input.AllowedSubjectTypes},
	}

	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, Ne{Left: Col{Table: "s", Column: "subject_id"}, Right: Lit("*")})
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(input.SubjectRelation)},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject: SubjectRef{
				Type: subjectTypeExpr,
				ID:   Col{Table: "s", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, objectIDExpr),
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}

type ListSubjectsUsersetPatternComplexInput struct {
	ObjectType       string
	SubjectType      string
	SubjectRelation  string
	SourceRelations  []string
	ObjectIDExpr     string
	SubjectTypeExpr  string
	IsClosurePattern bool
	SourceRelation   string
	Exclusions       ExclusionConfig
}

func ListSubjectsUsersetPatternComplexQuery(input ListSubjectsUsersetPatternComplexInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(input.SubjectRelation)},
	}

	if input.IsClosurePattern {
		conditions = append(conditions, CheckPermission{
			Subject: SubjectRef{
				Type: subjectTypeExpr,
				ID:   Col{Table: "s", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, objectIDExpr),
			ExpectAllow: true,
		})
	}

	// Use lateral join with list function
	listFuncName := "list_" + Ident(input.SubjectType) + "_" + Ident(input.SubjectRelation) + "_subjects"
	listFunc := LateralFunction{
		Name:  listFuncName,
		Args:  []Expr{UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, subjectTypeExpr},
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
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(input.ObjectType)},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: input.SourceRelations},
			And(conditions...),
		),
	}

	// Add exclusion predicates to WHERE
	predicates := input.Exclusions.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]Expr{stmt.Where}, predicates...)
		stmt.Where = And(allPredicates...)
	}

	return stmt.SQL(), nil
}

type ListSubjectsUsersetPatternRecursiveComplexInput struct {
	ObjectType          string
	SubjectType         string
	SubjectRelation     string
	SourceRelations     []string
	ObjectIDExpr        string
	SubjectTypeExpr     string
	AllowedSubjectTypes []string
	ExcludeWildcard     bool
	IsClosurePattern    bool
	SourceRelation      string
	Exclusions          ExclusionConfig
}

func ListSubjectsUsersetPatternRecursiveComplexQuery(input ListSubjectsUsersetPatternRecursiveComplexInput) (string, error) {
	objectIDExpr := stringToDSLExpr(input.ObjectIDExpr)
	subjectTypeExpr := stringToDSLExpr(input.SubjectTypeExpr)

	// Join conditions for the membership table
	joinConditions := []Expr{
		Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(input.SubjectType)},
		Eq{Left: Col{Table: "m", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: subjectTypeExpr},
		In{Expr: subjectTypeExpr, Values: input.AllowedSubjectTypes},
	}

	if input.ExcludeWildcard {
		joinConditions = append(joinConditions, Ne{Left: Col{Table: "m", Column: "subject_id"}, Right: Lit("*")})
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: objectIDExpr},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(input.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(input.SubjectRelation)},
		CheckPermission{
			Subject: SubjectRef{
				Type: subjectTypeExpr,
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
				Type: subjectTypeExpr,
				ID:   Col{Table: "m", Column: "subject_id"},
			},
			Relation:    input.SourceRelation,
			Object:      LiteralObject(input.ObjectType, objectIDExpr),
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

	// Add exclusion predicates
	for _, pred := range input.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return q.SQL(), nil
}
