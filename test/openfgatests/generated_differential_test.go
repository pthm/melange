package openfgatests_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/openfga/openfga/pkg/typesystem"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/test/openfgatests"
	"github.com/pthm/melange/test/testutil"
)

// TestOpenFGA_GeneratedAlgebra compares generated SQL with a real OpenFGA
// evaluator, not with another Melange API or fixture-provided expectations.
// Models are bounded acyclic graphs; all 128 subsets of the seven possible
// tuples are tested for each accepted model. OpenFGA's independent validator
// defines the valid-model domain; Melange compilation/evaluation errors are
// always failures. The seed and full counterexample are replayable.
func TestOpenFGA_GeneratedAlgebra(t *testing.T) {
	if testing.Short() {
		t.Skip("generated differential testing requires PostgreSQL")
	}
	seed := algebraEnvInt(t, "MELANGE_ALGEBRA_SEED", 1)
	models := algebraEnvInt(t, "MELANGE_ALGEBRA_MODELS", 12)
	require.Positive(t, models)
	masks := make([]uint8, 128)
	for j := range masks {
		masks[j] = uint8(j)
	}
	if os.Getenv("MELANGE_ALGEBRA_MASK") != "" {
		mask := algebraEnvInt(t, "MELANGE_ALGEBRA_MASK", 0)
		require.GreaterOrEqual(t, mask, 0)
		require.Less(t, mask, 128)
		masks = []uint8{uint8(mask)}
	}
	accepted := 0
	for i := 0; i < models*10 && accepted < models; i++ {
		modelSeed := seed + i
		if !t.Run(fmt.Sprintf("seed_%d", modelSeed), func(t *testing.T) {
			program := algebraProgram(int64(modelSeed))
			if runAlgebraDifferential(t, program, masks) {
				accepted++
			}
		}) {
			return // Report one reproducible counterexample, rather than flooding logs.
		}
	}
	require.Equal(t, models, accepted, "not enough OpenFGA-valid generated models")
}

// FuzzAuthorizationAlgebra lets Go mutate model choices and tuple assignments
// and minimize a failing input. Run with -parallel=1 to bound database creation.
func FuzzAuthorizationAlgebra(f *testing.F) {
	if testing.Short() {
		f.Skip("generated differential testing requires PostgreSQL")
	}
	for seed := int64(1); seed <= 4; seed++ {
		f.Add(algebraProgram(seed), uint8(seed*29))
	}
	f.Fuzz(func(t *testing.T, program []byte, mask uint8) {
		if !runAlgebraDifferential(t, program, []uint8{mask & 127}) {
			t.Skip("outside the OpenFGA-valid model domain")
		}
	})
}

func algebraEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	require.NoError(t, err, name)
	return n
}

func algebraProgram(seed int64) []byte {
	rng := rand.New(rand.NewSource(seed)) // Deterministic test input, not cryptography.
	program := make([]byte, 64)
	_, _ = rng.Read(program)
	return program
}

type algebraModel struct {
	dsl       string
	relations map[string][]string
}

// generateAlgebraModel uses only backward references, so every graph is acyclic
// and every relation has a path to a directly assignable relation. Both operand
// positions can reference an earlier expression, including the subtract side
// of an exclusion. Parent inheritance can appear anywhere in the document's
// algebra by referring to an inherited relation.
func generateAlgebraModel(program []byte) algebraModel {
	pos := 0
	take := func() int {
		if pos >= len(program) {
			return 0
		}
		v := int(program[pos])
		pos++
		return v
	}
	var b strings.Builder
	b.WriteString("model\n  schema 1.1\ntype user\n")
	model := algebraModel{relations: make(map[string][]string)}
	for _, objectType := range []string{"folder", "document"} {
		fmt.Fprintf(&b, "type %s\n  relations\n", objectType)
		pool := []string{"a", "b", "c"}
		for _, rel := range pool {
			fmt.Fprintf(&b, "    define %s: [user]\n", rel)
		}
		if objectType == "document" {
			b.WriteString("    define parent: [folder]\n")
			for i := 0; i < 3; i++ {
				name := fmt.Sprintf("inherited%d", i)
				fmt.Fprintf(&b, "    define %s: r%d from parent\n", name, i)
				pool = append(pool, name)
			}
		}
		// Four to seven computed relations. Rotate through every operator,
		// then vary operands and nesting; aliases exercise closure expansion.
		count, offset := 4+take()%4, take()%4
		for i := 0; i < count; i++ {
			left, right := pool[take()%len(pool)], pool[take()%len(pool)]
			if i > 0 {
				if take()%2 == 0 {
					left = fmt.Sprintf("r%d", i-1)
				} else {
					right = fmt.Sprintf("r%d", i-1)
				}
			}
			name := fmt.Sprintf("r%d", i)
			var expression string
			switch (i + offset) % 4 {
			case 0:
				expression = left + " or " + right
			case 1:
				expression = left + " and " + right
			case 2:
				expression = left + " but not " + right
			case 3:
				expression = fmt.Sprintf("r%d", i-1)
				if i == 0 {
					expression = left
				}
			}
			fmt.Fprintf(&b, "    define %s: %s\n", name, expression)
			pool = append(pool, name)
		}
		model.relations[objectType] = pool
	}
	model.dsl = b.String()
	return model
}

// Each mask gets disconnected objects, so loading all assignments together
// cannot change their answers. Bits 0..2 grant a/b/c on the document, bits
// 3..5 grant a/b/c on its folder, and bit 6 connects document to folder.
func algebraTuples(mask uint8) []*openfgav1.TupleKey {
	id := fmt.Sprintf("case%d", mask)
	var tuples []*openfgav1.TupleKey
	for i := 0; i < 6; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		objectType := "document"
		if i >= 3 {
			objectType = "folder"
		}
		tuples = append(tuples, &openfgav1.TupleKey{
			User: "user:alice", Relation: []string{"a", "b", "c"}[i%3], Object: objectType + ":" + id,
		})
	}
	if mask&64 != 0 {
		tuples = append(tuples, &openfgav1.TupleKey{
			User: "folder:" + id, Relation: "parent", Object: "document:" + id,
		})
	}
	return tuples
}

func runAlgebraDifferential(t *testing.T, program []byte, masks []uint8) bool {
	t.Helper()
	model := generateAlgebraModel(program)
	// Log only on failure (unless -v), including failures during compilation or
	// oracle validation. Go fuzzing also saves and shrinks the original input.
	t.Logf("model choices: %x\n%s", program, model.dsl)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Validate and evaluate the original model through OpenFGA's real server.
	// It never receives Melange's flattened model or generated SQL.
	protoModel, err := transformer.TransformDSLToProto(model.dsl)
	require.NoError(t, err, "generator produced invalid DSL")
	if _, err := typesystem.NewAndValidate(ctx, protoModel); err != nil {
		t.Logf("outside valid-model domain: %v", err)
		return false
	}
	oracle := openfgatests.NewOpenFGAClient(t)
	store, err := oracle.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: "generated-algebra"})
	require.NoError(t, err)
	written, err := oracle.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId: store.GetId(), SchemaVersion: "1.1", TypeDefinitions: protoModel.GetTypeDefinitions(),
	})
	require.NoError(t, err, "generator produced an invalid OpenFGA model")

	db := testutil.EmptyDB(t)
	_, err = db.ExecContext(ctx, `CREATE TABLE melange_tuples (
		subject_type text, subject_id text, relation text, object_type text, object_id text
	)`)
	require.NoError(t, err)
	types, err := parser.ParseSchemaString(model.dsl)
	require.NoError(t, err, "Melange rejected an OpenFGA-valid model")
	require.NoError(t, migrator.NewMigrator(db, "").MigrateWithTypes(ctx, types))

	var tuples []*openfgav1.TupleKey
	for _, mask := range masks {
		tuples = append(tuples, algebraTuples(mask)...)
	}
	for start := 0; start < len(tuples); start += 40 {
		_, err = oracle.Write(ctx, &openfgav1.WriteRequest{
			StoreId: store.GetId(), AuthorizationModelId: written.GetAuthorizationModelId(),
			Writes: &openfgav1.WriteRequestWrites{TupleKeys: tuples[start:min(start+40, len(tuples))]},
		})
		require.NoError(t, err, "generator produced invalid tuples")
	}
	insertAlgebraTuples(t, ctx, db, tuples)

	var allows, denies int
	for _, mask := range masks {
		for _, objectType := range []string{"folder", "document"} {
			id := fmt.Sprintf("case%d", mask)
			for _, relation := range model.relations[objectType] {
				for _, user := range []string{"alice", "stranger"} {
					query := &openfgav1.CheckRequestTupleKey{User: "user:" + user, Relation: relation, Object: objectType + ":" + id}
					want, err := oracle.Check(ctx, &openfgav1.CheckRequest{
						StoreId: store.GetId(), AuthorizationModelId: written.GetAuthorizationModelId(), TupleKey: query,
						Consistency: openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY,
					})
					require.NoError(t, err, "oracle check failed: mask=%d query=%v", mask, query)
					var got int
					err = db.QueryRowContext(ctx, `SELECT check_permission('user', $1, $2, $3, $4)`, user, relation, objectType, id).Scan(&got)
					require.NoError(t, err, "Melange check failed: mask=%d query=%v", mask, query)
					if got != 0 && got != 1 || (got == 1) != want.GetAllowed() {
						t.Fatalf("authorization semantics mismatch\nmask=%d\ntuples=%v\nquery=%v\nOpenFGA=%t Melange=%d\nreplay with FuzzAuthorizationAlgebra input: (%x, %d)",
							mask, algebraTuples(mask), query, want.GetAllowed(), got, program, mask)
					}
					if want.GetAllowed() {
						allows++
					} else {
						denies++
					}
				}
			}
		}
	}
	t.Logf("compared %d allows and %d denies across %d assignments", allows, denies, len(masks))
	if len(masks) == 128 {
		require.Positive(t, allows, "vacuous exhaustive run")
		require.Positive(t, denies, "vacuous exhaustive run")
	}
	return true
}

func insertAlgebraTuples(t *testing.T, ctx context.Context, db *sql.DB, tuples []*openfgav1.TupleKey) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO melange_tuples VALUES ($1, $2, $3, $4, $5)`)
	require.NoError(t, err)
	defer func() { _ = stmt.Close() }()
	for _, tuple := range tuples {
		st, sid, _ := strings.Cut(tuple.GetUser(), ":")
		ot, oid, _ := strings.Cut(tuple.GetObject(), ":")
		_, err := stmt.ExecContext(ctx, st, sid, tuple.GetRelation(), ot, oid)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}
