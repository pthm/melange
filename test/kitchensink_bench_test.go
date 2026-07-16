package test

import (
	"database/sql"
	"os"
	"strings"
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

	// Non-plain query subjects — the surfaces the F4 optimization touches and the
	// subject types the original benches never exercised.
	svcAccount string // a service_account subject (second concrete principal)
	svcGroup   string // a group the service account is a direct member of
	// A userset-typed subject (subject_id contains '#') and an object it is
	// granted on. Drives the Case-2 computed-userset path + the F4 sargable join.
	usersetSubj    string // e.g. group:gX#member
	usersetObj     string // e.g. organization:oY where the membership holds
	usersetRel     string // relation to check on that object (member)
	usersetObjType string // organization
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
	// A userset-typed subject (group:gX#member) that is a member of some org: this
	// (subject, object) pair makes check/list route through the Case-2
	// computed-userset path and the F4 sargable join.
	var usSubj, usObj string
	if err := db.QueryRow(`SELECT subject_id, object_id FROM kitchen_sink_tuples
		WHERE subject_type='group' AND subject_id LIKE '%#member'
		AND relation='member' AND object_type='organization' ORDER BY object_id LIMIT 1`).Scan(&usSubj, &usObj); err != nil {
		b.Fatalf("query userset (subject,object) pair: %v", err)
	}

	// A service_account and a group it is a direct member of: pick both from one
	// tuple so they're guaranteed to check=1 (not every service_account has a
	// group grant in the generator).
	var svcAcct, svcGrp string
	if err := db.QueryRow(`SELECT subject_id, object_id FROM kitchen_sink_tuples
		WHERE subject_type='service_account' AND subject_id <> '*'
		AND relation='member' AND object_type='group' ORDER BY subject_id, object_id LIMIT 1`).Scan(&svcAcct, &svcGrp); err != nil {
		b.Fatalf("query service_account group membership: %v", err)
	}

	return &kitchenSinkBenchData{
		db:      db,
		subject: one(`SELECT subject_id FROM kitchen_sink_tuples WHERE object_type='organization' AND relation='owner' AND subject_type='user' LIMIT 1`),
		member:  one(`SELECT subject_id FROM kitchen_sink_tuples WHERE object_type='organization' AND relation='member' AND subject_type='user' AND subject_id <> '*' LIMIT 1`),
		doc:     one(`SELECT object_id FROM kitchen_sink_tuples WHERE object_type='document' ORDER BY object_id LIMIT 1`),
		folder:  one(`SELECT object_id FROM kitchen_sink_tuples WHERE object_type='folder' ORDER BY object_id LIMIT 1`),

		svcAccount:     svcAcct,
		svcGroup:       svcGrp,
		usersetSubj:    usSubj,
		usersetObj:     usObj,
		usersetRel:     "member",
		usersetObjType: "organization",
	}
}

// BenchmarkKitchenSinkCheck measures check_permission across the subject-type
// matrix: plain user (deep inherited), a userset-typed subject (the F4
// Case-2 computed-userset + sargable-join path), a service_account, and a
// wildcard subject.
func BenchmarkKitchenSinkCheck(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)

			// PlainUser: deep inherited relation over a plain user subject.
			b.Run("PlainUser", func(b *testing.B) {
				benchCheck(b, d.db, "user", d.subject, "can_view", "document", d.doc)
			})
			// UsersetSubject: subject_id contains '#' — the ONLY shape that
			// exercises the F4 sargable join + Case-2 computed-userset removal.
			b.Run("UsersetSubject", func(b *testing.B) {
				benchCheck(b, d.db, "group", d.usersetSubj, d.usersetRel, d.usersetObjType, d.usersetObj)
			})
			// ServiceAccount: the second concrete principal type.
			b.Run("ServiceAccount", func(b *testing.B) {
				benchCheck(b, d.db, "service_account", d.svcAccount, "member", "group", d.svcGroup)
			})
			// Wildcard: user:* against a relation with a public wildcard grant.
			b.Run("Wildcard", func(b *testing.B) {
				benchCheck(b, d.db, "user", "*", "can_view", "document", d.doc)
			})
		})
	}
}

// benchCheck runs check_permission in a tight loop.
func benchCheck(b *testing.B, db *sql.DB, st, sid, rel, ot, oid string) {
	b.Helper()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var r int
		if err := db.QueryRow(`SELECT check_permission($1,$2,$3,$4,$5)`, st, sid, rel, ot, oid).Scan(&r); err != nil {
			b.Fatal(err)
		}
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
		{"DocCanEdit", "can_edit", "document", "subject"}, // intersection: editor and active
		{"DocGated", "gated", "document", "subject"},      // intersection: viewer and active
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
			// UsersetSubject: query subject is a userset (group:gX#member) — the
			// F4 sargable-join path in list_*_obj that no plain-subject case hits.
			b.Run("UsersetSubject_OrgMember", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					drainList(b, d.db, `SELECT object_id FROM list_accessible_objects('group',$1,$2,$3)`,
						d.usersetSubj, d.usersetRel, d.usersetObjType)
				}
			})
			// ServiceAccount subject.
			b.Run("ServiceAccount_GroupMember", func(b *testing.B) {
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					drainList(b, d.db, `SELECT object_id FROM list_accessible_objects('service_account',$1,'member','group')`, d.svcAccount)
				}
			})
		})
	}
}

// BenchmarkKitchenSinkExpand measures expand_permission — the shallow
// UsersetTree surface. Exercises the F3 shared dispatchIfChain in the expand
// dispatcher, which no other benchmark touches.
func BenchmarkKitchenSinkExpand(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			// document.can_view: the heavy union+exclusion+wildcard+TTU relation.
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var j []byte
				if err := d.db.QueryRow(`SELECT expand_permission('document',$1,'can_view')`, d.doc).Scan(&j); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkKitchenSinkExplain measures explain_permission — the JSONB trace
// surface. Exercises the F3 short-circuit + shared dispatchIfChain in the
// explain dispatcher.
func BenchmarkKitchenSinkExplain(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var j []byte
				if err := d.db.QueryRow(`SELECT explain_permission('user',$1,'can_view','document',$2)`,
					d.subject, d.doc).Scan(&j); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkKitchenSinkBulk measures check_permission_bulk with a batch spanning
// multiple object types, exercising the per-type IF-block dispatcher.
func BenchmarkKitchenSinkBulk(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			org := oneID(b, d.db, `SELECT object_id FROM kitchen_sink_tuples WHERE object_type='organization' ORDER BY object_id LIMIT 1`)
			// Batch across document / folder / organization object types so more
			// than one per-type IF block fires.
			// Postgres array literals ({...}) per the proven bulk-check pattern;
			// no driver-specific array wrapper needed.
			sts := arrayLit("user", "user", "user")
			sids := arrayLit(d.subject, d.subject, d.subject)
			rels := arrayLit("can_view", "viewer", "member")
			ots := arrayLit("document", "folder", "organization")
			oids := arrayLit(d.doc, d.folder, org)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := d.db.Query(`SELECT idx, allowed FROM check_permission_bulk($1,$2,$3,$4,$5)`,
					sts, sids, rels, ots, oids)
				if err != nil {
					b.Fatal(err)
				}
				for rows.Next() {
					var idx, allowed int
					if err := rows.Scan(&idx, &allowed); err != nil {
						b.Fatal(err)
					}
				}
				_ = rows.Close()
			}
		})
	}
}

// arrayLit builds a Postgres text[] array literal. Kitchen-sink ids are simple
// (letters, digits, '_', '#', '*') so no quoting/escaping is required.
func arrayLit(xs ...string) string {
	return "{" + strings.Join(xs, ",") + "}"
}

// oneID scans a single string id.
func oneID(b *testing.B, db *sql.DB, q string, args ...any) string {
	b.Helper()
	var s string
	if err := db.QueryRow(q, args...).Scan(&s); err != nil {
		b.Fatalf("query id: %v", err)
	}
	return s
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
