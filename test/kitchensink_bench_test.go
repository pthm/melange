package test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/pthm/melange/test/testutil"
)

// kitchenSinkBenchData holds a migrated+loaded kitchenSink DB and representative ids.
type kitchenSinkBenchData struct {
	db      *sql.DB
	subject string // a user with broad inherited access (org owner)
	member  string // an org member
	doc     string // a representative document
	folder  string // a representative folder
}

func setupKitchenSinkBench(b *testing.B, scale testutil.KitchenSinkScale) *kitchenSinkBenchData {
	b.Helper()
	tuples := testutil.GenerateKitchenSinkTuples(scale)
	if len(tuples) > 200_000 && os.Getenv("MELANGE_BENCH_LARGE_SCALE") != "1" {
		b.Skipf("large kitchenSink scale %s (~%d tuples) requires MELANGE_BENCH_LARGE_SCALE=1", scale.Name, len(tuples))
	}

	db := testutil.SetupKitchenSinkDB(b)
	testutil.LoadKitchenSinkTuples(b, db, tuples)

	one := func(q string, args ...any) string {
		var s string
		if err := db.QueryRow(q, args...).Scan(&s); err != nil {
			b.Fatalf("query representative id: %v", err)
		}
		return s
	}
	return &kitchenSinkBenchData{
		db:      db,
		subject: one(`SELECT subject_id FROM kitchen_sink_tuples WHERE object_type='organization' AND relation='owner' AND subject_type='user' LIMIT 1`),
		member:  one(`SELECT subject_id FROM kitchen_sink_tuples WHERE object_type='organization' AND relation='member' AND subject_type='user' AND subject_id <> '*' LIMIT 1`),
		doc:     one(`SELECT object_id FROM kitchen_sink_tuples WHERE object_type='document' ORDER BY object_id LIMIT 1`),
		folder:  one(`SELECT object_id FROM kitchen_sink_tuples WHERE object_type='folder' ORDER BY object_id LIMIT 1`),
	}
}

// BenchmarkKitchenSinkCheck measures check_permission on a deep inherited relation.
func BenchmarkKitchenSinkCheck(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var r int
				if err := d.db.QueryRow(`SELECT check_permission('user',$1,'can_view','document',$2)`,
					d.subject, d.doc).Scan(&r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkKitchenSinkListObjects measures list_accessible_objects on the heaviest
// paths: document.can_view (union + exclusion + wildcard + multi-level TTU) and
// folder.viewer (self-referential recursive TTU + cross-type anchor).
func BenchmarkKitchenSinkListObjects(b *testing.B) {
	cases := []struct{ name, rel, objType, subjKey string }{
		{"DocCanView", "can_view", "document", "subject"},
		{"FolderViewer", "viewer", "folder", "subject"},
		{"DocEditor_Member", "editor", "document", "member"},
	}
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			for _, c := range cases {
				subj := d.subject
				if c.subjKey == "member" {
					subj = d.member
				}
				b.Run(c.name, func(b *testing.B) {
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						drainList(b, d.db, `SELECT object_id FROM list_accessible_objects('user',$1,$2,$3)`, subj, c.rel, c.objType)
					}
				})
			}
		})
	}
}

// BenchmarkKitchenSinkListSubjects measures list_accessible_subjects on document
// relations (recursive parent_closure + exclusion + wildcard).
func BenchmarkKitchenSinkListSubjects(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			for _, rel := range []string{"viewer", "can_view"} {
				b.Run(rel, func(b *testing.B) {
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						drainList(b, d.db, `SELECT subject_id FROM list_accessible_subjects('document',$1,$2,'user')`, d.doc, rel)
					}
				})
			}
		})
	}
}

func drainList(b *testing.B, db *sql.DB, q string, args ...any) {
	rows, err := db.Query(q, args...)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			b.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
}
