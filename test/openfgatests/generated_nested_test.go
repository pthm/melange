package openfgatests_test

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/openfga/openfga/pkg/typesystem"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/test/openfgatests"
	"github.com/pthm/melange/test/testutil"
)

// TestOpenFGA_GeneratedNested extends TestOpenFGA_GeneratedAlgebra to the
// shapes it leaves out: nested inline expressions (including direct
// assignment and tuple-to-userset operands), wildcards, userset type
// restrictions and subjects, recursive parent inheritance, and the list
// operations. Every Check, ListObjects and ListUsers answer is compared with
// a real in-process OpenFGA server.
//
// Tuple sets are sampled rather than enumerated: each model gets several
// random assignments on disconnected objects.
func TestOpenFGA_GeneratedNested(t *testing.T) {
	if testing.Short() {
		t.Skip("generated differential testing requires PostgreSQL")
	}
	seed := algebraEnvInt(t, "MELANGE_NESTED_SEED", 1)
	models := algebraEnvInt(t, "MELANGE_NESTED_MODELS", 12)
	require.Positive(t, models)
	accepted := 0
	for i := 0; i < models*10 && accepted < models; i++ {
		modelSeed := seed + i
		if !t.Run(fmt.Sprintf("seed_%d", modelSeed), func(t *testing.T) {
			if runNestedDifferential(t, algebraProgram(int64(modelSeed))) {
				accepted++
			}
		}) {
			return // Report one reproducible counterexample.
		}
	}
	require.Equal(t, models, accepted, "not enough OpenFGA-valid generated models")
}

// FuzzNestedAlgebra lets Go mutate the generator's choices. Run with
// -parallel=1 to bound database creation.
func FuzzNestedAlgebra(f *testing.F) {
	if testing.Short() {
		f.Skip("generated differential testing requires PostgreSQL")
	}
	for seed := int64(1); seed <= 4; seed++ {
		f.Add(algebraProgram(seed))
	}
	f.Fuzz(func(t *testing.T, program []byte) {
		if !runNestedDifferential(t, program) {
			t.Skip("outside the OpenFGA-valid model domain")
		}
	})
}

// nestedSamples is the number of tuple assignments per model.
const nestedSamples = 6

type nestedGen struct {
	program []byte
	pos     int
	rest    *rand.Rand // choices past the end of program, seeded from it
}

func (g *nestedGen) take() int {
	if g.pos < len(g.program) {
		v := int(g.program[g.pos])
		g.pos++
		return v
	}
	if g.rest == nil {
		h := fnv.New64a()
		_, _ = h.Write(g.program)
		g.rest = rand.New(rand.NewSource(int64(h.Sum64()))) // Deterministic test input.
	}
	return g.rest.Intn(256)
}

// expr builds a random expression over earlier relations and parent
// lookups. Only acyclic references are generated; recursion comes from the
// folder's `v` relation.
func (g *nestedGen) expr(pool []string, depth int, leftmost bool) string {
	if depth == 0 || g.take()%3 == 0 {
		// A direct type restriction is only valid as the leftmost operand.
		if leftmost && g.take()%5 == 0 {
			return "[user]"
		}
		// Parent lookups in compound expressions are often rejected (see
		// pkg/parser/normalize.go), so keep them to about a quarter of atoms.
		if g.take()%4 == 0 {
			return []string{"a from parent", "v from parent", "b from parent"}[g.take()%3]
		}
		return pool[g.take()%len(pool)]
	}
	op := []string{" or ", " and ", " but not "}[g.take()%3]
	return "(" + g.expr(pool, depth-1, leftmost) + op + g.expr(pool, depth-1, false) + ")"
}

type nestedModel struct {
	dsl     string
	derived []string // document relations generated as expressions
	direct  []string // document relations that accept [user] tuples
}

func (g *nestedGen) model() nestedModel {
	var b strings.Builder
	b.WriteString(`model
  schema 1.1
type user
type group
  relations
    define member: [user, user:*]
type folder
  relations
    define parent: [folder]
    define a: [user]
    define b: [user, user:*]
    define v: [user] or v from parent
type document
  relations
    define parent: [folder]
    define a: [user]
    define b: [user, user:*]
    define c: [user, group#member]
`)
	m := nestedModel{direct: []string{"a", "b", "c"}}
	count := 3 + g.take()%3
	pool := append(make([]string, 0, 3+count), "a", "b", "c")
	for i := range count {
		name := fmt.Sprintf("r%d", i)
		e := g.expr(pool, 3, true)
		e = strings.TrimSuffix(strings.TrimPrefix(e, "("), ")")
		if strings.Contains(e, "[user]") {
			m.direct = append(m.direct, name)
		}
		fmt.Fprintf(&b, "    define %s: %s\n", name, e)
		m.derived = append(m.derived, name)
		pool = append(pool, name)
	}
	m.dsl = b.String()
	return m
}

// tuples samples one assignment on objects suffixed with id. Objects of
// different samples are disconnected, so all samples can be loaded at once.
func (g *nestedGen) tuples(m nestedModel, id string) []*openfgav1.TupleKey {
	doc, f1, f0, grp := "document:d"+id, "folder:f1_"+id, "folder:f0_"+id, "group:g"+id
	candidates := make([]*openfgav1.TupleKey, 0, 7+2*(len(m.direct)+6))
	candidates = append(candidates, []*openfgav1.TupleKey{
		{User: f1, Relation: "parent", Object: doc},
		{User: f0, Relation: "parent", Object: f1},
		{User: "user:alice", Relation: "member", Object: grp},
		{User: "user:*", Relation: "member", Object: grp},
		{User: "user:*", Relation: "b", Object: doc},
		{User: "user:*", Relation: "b", Object: f1},
		{User: grp + "#member", Relation: "c", Object: doc},
	}...)
	for _, user := range []string{"user:alice", "user:bob"} {
		for _, rel := range m.direct {
			candidates = append(candidates, &openfgav1.TupleKey{User: user, Relation: rel, Object: doc})
		}
		for _, rel := range []string{"a", "b", "v"} {
			candidates = append(candidates,
				&openfgav1.TupleKey{User: user, Relation: rel, Object: f1},
				&openfgav1.TupleKey{User: user, Relation: rel, Object: f0})
		}
	}
	var out []*openfgav1.TupleKey
	for _, tk := range candidates {
		if g.take()%3 == 0 {
			out = append(out, tk)
		}
	}
	return out
}

func runNestedDifferential(t *testing.T, program []byte) bool {
	t.Helper()
	g := &nestedGen{program: program}
	m := g.model()
	t.Logf("model choices: %x\n%s", program, m.dsl)
	all := make([]*openfgav1.TupleKey, 0, nestedSamples*16)
	ids := make([]string, nestedSamples)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
		all = append(all, g.tuples(m, ids[i])...)
	}
	relations := append([]string{"a", "b", "c"}, m.derived...)
	return compareNested(t, m.dsl, relations, ids, all, fmt.Sprintf("FuzzNestedAlgebra input: %x", program))
}

// compareNested loads dsl and tuples into OpenFGA and melange and compares
// every Check, ListUsers and ListObjects answer for the document relations
// on the sample objects named by ids (document:d<id>, group:g<id>). It
// returns false when the model is outside the domain both accept.
func compareNested(t *testing.T, dsl string, relations, ids []string, all []*openfgav1.TupleKey, replay string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	protoModel, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		t.Logf("outside valid-model domain (DSL): %v", err)
		return false
	}
	if _, err := typesystem.NewAndValidate(ctx, protoModel); err != nil {
		t.Logf("outside valid-model domain: %v", err)
		return false
	}
	types, err := parser.ParseSchemaString(dsl)
	if melange.IsInvalidSchemaErr(err) && strings.Contains(err.Error(), "cannot compile exactly") {
		t.Logf("melange rejects this shape by design: %v", err)
		return false
	}
	require.NoError(t, err, "Melange rejected an OpenFGA-valid model")

	oracle := openfgatests.NewOpenFGAClient(t)
	store, err := oracle.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: "generated-nested"})
	require.NoError(t, err)
	written, err := oracle.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId: store.GetId(), SchemaVersion: "1.1", TypeDefinitions: protoModel.GetTypeDefinitions(),
	})
	require.NoError(t, err)
	storeID, modelID := store.GetId(), written.GetAuthorizationModelId()

	db := testutil.EmptyDB(t)
	_, err = db.ExecContext(ctx, `CREATE TABLE melange_tuples (
		subject_type text, subject_id text, relation text, object_type text, object_id text
	)`)
	require.NoError(t, err)
	require.NoError(t, migrator.NewMigrator(db, "").MigrateWithTypes(ctx, types))

	for start := 0; start < len(all); start += 40 {
		_, err = oracle.Write(ctx, &openfgav1.WriteRequest{
			StoreId: storeID, AuthorizationModelId: modelID,
			Writes: &openfgav1.WriteRequestWrites{TupleKeys: all[start:min(start+40, len(all))]},
		})
		require.NoError(t, err, "generator produced invalid tuples")
	}
	insertAlgebraTuples(t, ctx, db, all)

	mismatch := func(kind string, query, want, got any) {
		t.Fatalf("%s mismatch\nquery=%v\nOpenFGA=%v\nMelange=%v\ntuples=%v\nreplay with %s",
			kind, query, want, got, all, replay)
	}
	// oracleCheck asks OpenFGA's Check.
	oracleCheck := func(user, rel, object string) (bool, error) {
		res, err := oracle.Check(ctx, &openfgav1.CheckRequest{
			StoreId: storeID, AuthorizationModelId: modelID,
			TupleKey:    &openfgav1.CheckRequestTupleKey{User: user, Relation: rel, Object: object},
			Consistency: openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY,
		})
		return res.GetAllowed(), err
	}
	// unexplained drops the elements of the symmetric difference whose
	// membership melange's answer and OpenFGA's Check agree on. OpenFGA's
	// list answers are not always consistent with its own Check (e.g.
	// ListUsers ignoring an exclusion reached through a wildcard group
	// member); a list difference that Check sides with melange on is
	// logged, not failed.
	unexplained := func(want, got []string, check func(elem string) (bool, bool)) []string {
		var out []string
		for _, e := range slices.Concat(want, got) {
			if slices.Contains(want, e) == slices.Contains(got, e) || slices.Contains(out, e) {
				continue
			}
			if allowed, ok := check(e); ok && allowed == slices.Contains(got, e) {
				t.Logf("OpenFGA's list disagrees with its own Check on %s; melange agrees with Check", e)
				continue
			}
			out = append(out, e)
		}
		return out
	}
	compared := 0

	for _, id := range ids {
		doc := "d" + id
		subjects := []string{"user:alice", "user:bob", "user:stranger", "group:g" + id + "#member"}
		for _, rel := range relations {
			for _, subject := range subjects {
				query := fmt.Sprintf("check %s %s document:%s", subject, rel, doc)
				allowed, err := oracleCheck(subject, rel, "document:"+doc)
				if err != nil {
					t.Logf("oracle declined %s: %v", query, err)
					continue
				}
				st, sid, _ := strings.Cut(subject, ":")
				var got int
				err = db.QueryRowContext(ctx, `SELECT check_permission($1, $2, $3, 'document', $4)`, st, sid, rel, doc).Scan(&got)
				require.NoError(t, err, "Melange check failed: %s", query)
				if (got == 1) != allowed {
					mismatch("check", query, allowed, got)
				}
				compared++
			}

			for _, filter := range []string{"user", "group#member"} {
				ft, fr, _ := strings.Cut(filter, "#")
				want, err := oracle.ListUsers(ctx, &openfgav1.ListUsersRequest{
					StoreId: storeID, AuthorizationModelId: modelID, Relation: rel,
					Object:      &openfgav1.Object{Type: "document", Id: doc},
					UserFilters: []*openfgav1.UserTypeFilter{{Type: ft, Relation: fr}},
					Consistency: openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY,
				})
				query := fmt.Sprintf("listusers document:%s#%s filter=%s", doc, rel, filter)
				if err != nil {
					t.Logf("oracle declined %s: %v", query, err)
					continue
				}
				wantSet := oracleUsers(want.GetUsers())
				gotSet := queryStrings(t, ctx, db,
					`SELECT subject_id FROM list_accessible_subjects('document', $1, $2, $3)`, doc, rel, filter)
				for i, s := range gotSet {
					gotSet[i] = ft + ":" + s
				}
				slices.Sort(gotSet)
				if !sameListedUsers(wantSet, gotSet) {
					diff := unexplained(wantSet, gotSet, func(user string) (bool, bool) {
						if strings.HasSuffix(user, ":*") {
							return false, false
						}
						allowed, err := oracleCheck(user, rel, "document:"+doc)
						return allowed, err == nil
					})
					if len(diff) > 0 {
						mismatch("listusers", query, wantSet, gotSet)
					}
				}
				compared++
			}
		}
	}

	for _, rel := range relations {
		for _, subject := range []string{"user:alice", "user:bob", "group:g0#member"} {
			want, err := oracle.ListObjects(ctx, &openfgav1.ListObjectsRequest{
				StoreId: storeID, AuthorizationModelId: modelID, Type: "document", Relation: rel, User: subject,
				Consistency: openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY,
			})
			query := fmt.Sprintf("listobjects %s %s document", subject, rel)
			if err != nil {
				t.Logf("oracle declined %s: %v", query, err)
				continue
			}
			wantSet := slices.Sorted(slices.Values(want.GetObjects()))
			st, sid, _ := strings.Cut(subject, ":")
			gotSet := queryStrings(t, ctx, db,
				`SELECT 'document:' || object_id FROM list_accessible_objects($1, $2, $3, 'document')`, st, sid, rel)
			slices.Sort(gotSet)
			if !slices.Equal(wantSet, gotSet) {
				diff := unexplained(wantSet, gotSet, func(object string) (bool, bool) {
					allowed, err := oracleCheck(subject, rel, object)
					return allowed, err == nil
				})
				if len(diff) > 0 {
					mismatch("listobjects", query, wantSet, gotSet)
				}
			}
			compared++
		}
	}
	t.Logf("compared %d answers across %d tuple assignments", compared, len(ids))
	return true
}

// sameListedUsers compares ListUsers answers. When both include user:*,
// every user has access, so which explicit users are also listed is a
// presentation difference (melange lists any candidate the check admits;
// OpenFGA only those reached through a tuple path). Usersets must match.
func sameListedUsers(want, got []string) bool {
	if slices.Contains(want, "user:*") && slices.Contains(got, "user:*") {
		explicit := func(s string) bool { return strings.HasPrefix(s, "user:") && s != "user:*" }
		want = slices.DeleteFunc(slices.Clone(want), explicit)
		got = slices.DeleteFunc(slices.Clone(got), explicit)
	}
	return slices.Equal(want, got)
}

func oracleUsers(users []*openfgav1.User) []string {
	out := make([]string, 0, len(users))
	for _, u := range users {
		switch {
		case u.GetObject() != nil:
			out = append(out, u.GetObject().GetType()+":"+u.GetObject().GetId())
		case u.GetUserset() != nil:
			us := u.GetUserset()
			out = append(out, us.GetType()+":"+us.GetId()+"#"+us.GetRelation())
		case u.GetWildcard() != nil:
			out = append(out, u.GetWildcard().GetType()+":*")
		}
	}
	slices.Sort(out)
	return out
}

func queryStrings(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	require.NoError(t, err, "Melange query failed: %s %v", query, args)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}
