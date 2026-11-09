package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// parseWhatIf parses one "subject relation object" tuple per line, skipping
// blank lines. With a model (types non-nil) it also rejects tuples the
// generated SQL would silently ignore: an unknown relation, or a subject type
// the relation does not accept directly. Every bad line is reported at once.
func parseWhatIf(raw string, types []schema.TypeDefinition) ([]melange.ContextualTuple, error) {
	var tuples []melange.ContextualTuple
	var errs []string
	for i, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		t, err := parseTupleLine(line, types)
		if err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", i+1, err))
			continue
		}
		tuples = append(tuples, t)
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "\n"))
	}
	return tuples, nil
}

func parseTupleLine(line string, types []schema.TypeDefinition) (melange.ContextualTuple, error) {
	f := strings.Fields(line)
	if len(f) != 3 {
		return melange.ContextualTuple{}, fmt.Errorf("expected <subject> <relation> <object>, got %q", strings.TrimSpace(line))
	}
	subject, err1 := parseIdent("subject", f[0])
	object, err2 := parseIdent("object", f[2])
	if err := firstErr(err1, err2); err != nil {
		return melange.ContextualTuple{}, err
	}
	t := melange.ContextualTuple{Subject: subject, Relation: melange.Relation(f[1]), Object: object}
	if types == nil {
		return t, nil
	}
	return t, validateTuple(types, t)
}

// validateTuple ports test/openfgatests' ValidateContextualTuple (test-only,
// so not importable). A userset subject is "type:id#relation" in the ID, as
// melange stores it.
func validateTuple(types []schema.TypeDefinition, t melange.ContextualTuple) error {
	rel, ok := findRelation(types, string(t.Object.Type), string(t.Relation))
	if !ok {
		return fmt.Errorf("relation %s not defined on %s", t.Relation, t.Object.Type)
	}
	subjType := string(t.Subject.Type)
	if !typeExists(types, subjType) {
		return fmt.Errorf("type %s not found", subjType)
	}
	id, usersetRel, isUserset := strings.Cut(t.Subject.ID, "#")
	if isUserset {
		if id == "" || usersetRel == "" {
			return fmt.Errorf("subject: expected <type>:<id>#<relation>, got %q", t.Subject.String())
		}
		if _, ok := findRelation(types, subjType, usersetRel); !ok {
			return fmt.Errorf("relation %s not defined on %s", usersetRel, subjType)
		}
	}
	for _, ref := range rel.SubjectTypeRefs {
		if ref.Type == subjType && ref.Relation == usersetRel && (id != "*" || ref.Wildcard) {
			return nil
		}
	}
	return fmt.Errorf("subject %s not allowed for %s on %s (accepts %s)", describeSubject(subjType, usersetRel, id), t.Relation, t.Object.Type, acceptedRefs(rel))
}

// acceptedRefs lists the subjects a relation accepts directly, as written in
// the DSL ("user, user:*, group#member"), or "no direct subjects".
func acceptedRefs(rel schema.RelationDefinition) string {
	if len(rel.SubjectTypeRefs) == 0 {
		return "no direct subjects"
	}
	refs := make([]string, len(rel.SubjectTypeRefs))
	for i, ref := range rel.SubjectTypeRefs {
		refs[i] = ref.Type
		switch {
		case ref.Wildcard:
			refs[i] += ":*"
		case ref.Relation != "":
			refs[i] += "#" + ref.Relation
		}
	}
	return strings.Join(refs, ", ")
}

func describeSubject(typ, usersetRel, id string) string {
	switch {
	case usersetRel != "":
		return typ + "#" + usersetRel
	case id == "*":
		return typ + ":*"
	}
	return "type " + typ
}

func findRelation(types []schema.TypeDefinition, typ, rel string) (schema.RelationDefinition, bool) {
	for _, t := range types {
		if t.Name != typ {
			continue
		}
		for _, r := range t.Relations {
			if r.Name == rel {
				return r, true
			}
		}
	}
	return schema.RelationDefinition{}, false
}

func typeExists(types []schema.TypeDefinition, typ string) bool {
	for _, t := range types {
		if t.Name == typ {
			return true
		}
	}
	return false
}

// whatIfView returns the setup that adds tuples to melange_tuples for one
// Explain, run by sqlapi.Trace inside a transaction that is always rolled
// back.
//
// This is studio's deliberate exception to the read-only connection, with
// explain on demand (sqlapi.Trace) the other. The generated functions read
// melange_tuples unqualified, so a temp view shadows it (as
// Checker.CheckWithContextualTuples does), and CREATE TEMP VIEW is refused
// under default_transaction_read_only. Trace begins the transaction READ
// WRITE, which overrides that default for this transaction only, runs this
// DDL and DISCARD PLANS, then SET TRANSACTION READ ONLY before Explain.
// Nothing is ever committed.
//
// The DDL fails on a hot-standby replica or for a role without the TEMP
// privilege; the error says so, since a read-only console is often pointed
// at a replica. It wraps with %v, not %w, so failedAfter, which reduces a
// *pq.Error to its bare message, keeps the hint.
func (s *Server) whatIfView(tuples []melange.ContextualTuple) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, whatIfViewSQL(s.dbSchema, tuples)); err != nil {
			return fmt.Errorf("what-if needs a writable primary and the TEMP privilege on the database: creating the temporary view: %v", err)
		}
		return nil
	}
}

// whatIfViewSQL builds the temp view: the base tuples plus a VALUES list.
// Every value is quoted; tuples is non-empty. It mirrors
// Checker.setupContextualTuples (melange/checker.go), which Explain cannot
// use; keep the two in sync. dbSchema is always set here, so it stands in
// for the runtime's lookupTuplesSchema.
func whatIfViewSQL(dbSchema string, tuples []melange.ContextualTuple) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `CREATE TEMP VIEW melange_tuples AS
SELECT subject_type, subject_id, relation, object_type, object_id FROM %s.melange_tuples
UNION ALL
SELECT subject_type, subject_id, relation, object_type, object_id FROM (VALUES `, pq.QuoteIdentifier(dbSchema))
	for i, t := range tuples {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "(%s::text, %s::text, %s::text, %s::text, %s::text)",
			pq.QuoteLiteral(string(t.Subject.Type)), pq.QuoteLiteral(t.Subject.ID),
			pq.QuoteLiteral(string(t.Relation)),
			pq.QuoteLiteral(string(t.Object.Type)), pq.QuoteLiteral(t.Object.ID))
	}
	sb.WriteString(") AS ctx(subject_type, subject_id, relation, object_type, object_id)")
	return sb.String()
}
