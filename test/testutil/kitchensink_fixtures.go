package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// KitchenSinkTuple is one authorization tuple in the kitchen-sink dataset.
type KitchenSinkTuple struct {
	SubjectType string
	SubjectID   string
	Relation    string
	ObjectType  string
	ObjectID    string
}

// KitchenSinkScale parameterises the deterministic kitchenSink-tuple generator. The same
// scale always produces the same tuples (no randomness), so results are
// reproducible across runs and machines.
type KitchenSinkScale struct {
	Name            string
	Users           int
	ServiceAccounts int
	Groups          int // total groups, arranged into self-referential nesting chains
	GroupChainLen   int // nesting depth per chain (exercises self-ref userset CTE)
	Orgs            int
	MembersPerOrg   int
	TeamsPerOrg     int
	ProjectsPerOrg  int
	FolderDepth     int // folder tree depth (self-referential recursive TTU)
	FoldersPerLevel int
	DocsPerFolder   int
	PlatformAdmins  int
}

// KitchenSinkScaleSmall is sized for the differential correctness test: small enough
// to enumerate every (subject, object) pair against check_permission, large
// enough that every relation has both granted and denied results.
var KitchenSinkScaleSmall = KitchenSinkScale{
	Name: "small", Users: 40, ServiceAccounts: 6, Groups: 12, GroupChainLen: 4,
	Orgs: 4, MembersPerOrg: 12, TeamsPerOrg: 2, ProjectsPerOrg: 3,
	FolderDepth: 4, FoldersPerLevel: 2, DocsPerFolder: 3, PlatformAdmins: 2,
}

// KitchenSinkScales are the benchmark sizes.
var KitchenSinkScales = []KitchenSinkScale{
	{Name: "1K", Users: 200, ServiceAccounts: 20, Groups: 40, GroupChainLen: 4, Orgs: 5, MembersPerOrg: 20, TeamsPerOrg: 3, ProjectsPerOrg: 5, FolderDepth: 4, FoldersPerLevel: 3, DocsPerFolder: 4, PlatformAdmins: 3},
	{Name: "10K", Users: 1000, ServiceAccounts: 80, Groups: 120, GroupChainLen: 5, Orgs: 15, MembersPerOrg: 40, TeamsPerOrg: 4, ProjectsPerOrg: 10, FolderDepth: 5, FoldersPerLevel: 4, DocsPerFolder: 6, PlatformAdmins: 5},
	{Name: "100K", Users: 5000, ServiceAccounts: 300, Groups: 400, GroupChainLen: 6, Orgs: 40, MembersPerOrg: 80, TeamsPerOrg: 6, ProjectsPerOrg: 20, FolderDepth: 6, FoldersPerLevel: 5, DocsPerFolder: 10, PlatformAdmins: 10},
	{Name: "1M", Users: 25000, ServiceAccounts: 1500, Groups: 2000, GroupChainLen: 6, Orgs: 100, MembersPerOrg: 200, TeamsPerOrg: 8, ProjectsPerOrg: 40, FolderDepth: 7, FoldersPerLevel: 6, DocsPerFolder: 16, PlatformAdmins: 25},
}

// user/service-account id helpers keep ids stable and cheap.
func uid(i int) string  { return fmt.Sprintf("u%d", i) }
func said(i int) string { return fmt.Sprintf("sa%d", i) }

// GenerateKitchenSinkTuples deterministically produces the tuple set for the kitchenSink
// schema at the given scale. It seeds every direct/linking relation so that
// list and check have non-trivial, overlapping results across the whole feature
// matrix. It does not attempt to be "semantically correct" — check_permission is
// the oracle in the differential test; the generator only needs varied grants.
func GenerateKitchenSinkTuples(s KitchenSinkScale) []KitchenSinkTuple {
	var t []KitchenSinkTuple
	add := func(st, sid, rel, ot, oid string) {
		t = append(t, KitchenSinkTuple{st, sid, rel, ot, oid})
	}
	umod := func(i int) string { return uid(i % max(s.Users, 1)) }

	// Platform singleton + admins.
	for i := 0; i < s.PlatformAdmins; i++ {
		add("user", uid(i), "admin", "platform", "main")
	}

	// mailing_list (simple userset source): users + service accounts subscribe.
	for i := 0; i < max(s.Groups, 1); i++ {
		add("user", umod(i), "subscriber", "mailing_list", fmt.Sprintf("ml%d", i))
		if s.ServiceAccounts > 0 {
			add("service_account", said(i%s.ServiceAccounts), "subscriber", "mailing_list", fmt.Sprintf("ml%d", i))
		}
	}

	// Groups arranged into self-referential nesting chains: g_i#member includes
	// g_{i+1}#member; the tail group holds direct users + a service account.
	for i := 0; i < s.Groups; i++ {
		g := fmt.Sprintf("g%d", i)
		if (i+1)%s.GroupChainLen != 0 && i+1 < s.Groups {
			add("group", fmt.Sprintf("g%d", i+1), "member", "group", g) // group#member userset
		} else {
			add("user", umod(i), "member", "group", g)
			add("user", umod(i+7), "member", "group", g)
			if s.ServiceAccounts > 0 {
				add("service_account", said(i%s.ServiceAccounts), "member", "group", g)
			}
		}
	}
	grp := func(i int) string { return fmt.Sprintf("g%d#member", absi(i)%max(s.Groups, 1)) }

	// Organizations: owner/admin/member (direct + via group userset) + guest wildcard.
	for o := 0; o < s.Orgs; o++ {
		org := fmt.Sprintf("o%d", o)
		add("user", umod(o), "owner", "organization", org)
		for m := 0; m < s.MembersPerOrg; m++ {
			ui := o*s.MembersPerOrg + m
			switch m % 4 {
			case 0:
				add("user", umod(ui), "admin", "organization", org)
			case 1:
				add("group", grp(ui), "member", "organization", org) // userset member
			default:
				add("user", umod(ui), "member", "organization", org)
			}
		}
		add("user", umod(o*3), "billing", "organization", org)
		if o%2 == 0 {
			add("user", "*", "guest", "organization", org) // wildcard grant
		}

		// Teams.
		for k := 0; k < s.TeamsPerOrg; k++ {
			team := fmt.Sprintf("t%d_%d", o, k)
			add("organization", org, "org", "team", team) // linking
			add("user", umod(o+k), "maintainer", "team", team)
			add("user", umod(o*7+k), "member", "team", team)
			add("group", grp(o*5+k), "member", "team", team) // userset member
		}

		// Projects (some public via wildcard).
		for p := 0; p < s.ProjectsPerOrg; p++ {
			proj := fmt.Sprintf("p%d_%d", o, p)
			add("organization", org, "org", "project", proj)
			add("user", umod(o+p), "admin", "project", proj)
			add("user", umod(o*3+p), "editor", "project", proj)
			add("group", grp(o*3+p), "editor", "project", proj)
			add("team", fmt.Sprintf("t%d_%d#member", o, p%max(s.TeamsPerOrg, 1)), "viewer", "project", proj)
			if p%3 == 0 {
				add("user", "*", "public", "project", proj)
				add("service_account", "*", "public", "project", proj)
			}
		}
	}

	// Folder trees: recursive parent (folder->folder) + cross-type (folder->project).
	proj := func(o, p int) string {
		return fmt.Sprintf("p%d_%d", o%max(s.Orgs, 1), absi(p)%max(s.ProjectsPerOrg, 1))
	}
	fname := func(o, level, idx int) string { return fmt.Sprintf("f%d_%d_%d", o, level, idx) }
	for o := 0; o < s.Orgs; o++ {
		org := fmt.Sprintf("o%d", o)
		for level := 0; level < s.FolderDepth; level++ {
			for idx := 0; idx < s.FoldersPerLevel; idx++ {
				f := fname(o, level, idx)
				if level == 0 {
					add("project", proj(o, idx), "parent", "folder", f) // cross-type parent
				} else {
					add("folder", fname(o, level-1, idx), "parent", "folder", f) // recursive parent
				}
				add("organization", org, "org", "folder", f)
				add("user", umod(o*11+level), "viewer", "folder", f)
				add("group", grp(o*7+level+idx), "viewer", "folder", f) // userset viewer
				add("user", umod(o*13+idx), "editor", "folder", f)
				if (level+idx)%4 == 0 {
					add("user", "*", "public", "folder", f) // wildcard reached via TTU
				}
				if (level+idx)%5 == 0 {
					add("user", umod(o*17+level), "banned", "folder", f)
				}

				// Documents under this folder.
				for d := 0; d < s.DocsPerFolder; d++ {
					doc := fmt.Sprintf("d%s_%d", f, d)
					add("folder", f, "folder", "document", doc)
					add("platform", "main", "platform", "document", doc)
					add("user", umod(o*19+d), "owner", "document", doc)
					add("user", umod(o*23+d), "editor", "document", doc)
					add("group", grp(o*11+d), "editor", "document", doc)
					add("folder", f+"#editor", "editor", "document", doc) // userset of other type
					add("user", umod(o*29+d), "viewer", "document", doc)
					add("group", grp(o*13+d), "viewer", "document", doc)
					add("user", umod(o*31+d), "active", "document", doc)
					add("user", umod(o*37+d), "reviewer", "document", doc)
					if d%3 == 0 {
						add("user", umod(o*41+d), "blocked", "document", doc)
					}
					if d%7 == 0 {
						add("user", "*", "blocked", "document", doc) // wildcard exclusion subject
					}
					if d%5 == 0 {
						add("user", umod(o*43+d), "restricted", "document", doc)
					}
					if d%4 == 0 {
						add("user", "*", "public", "document", doc)
					}

					// Reports + comments referencing the document.
					if d == 0 {
						rep := "r" + doc
						add("document", doc, "document", "report", rep)
						add("group", grp(o*3+d), "audience", "report", rep)
						com := "c" + doc
						add("document", doc, "document", "comment", com)
						add("user", umod(o*47+d), "author", "comment", com)
					}
				}
			}
		}
	}

	return t
}

func absi(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// SetupKitchenSinkDB returns a fresh database with the kitchen-sink schema migrated and a
// melange_tuples view over an (empty) kitchen_sink_tuples base table. Call
// LoadKitchenSinkTuples to populate it.
func SetupKitchenSinkDB(tb testing.TB) *sql.DB {
	tb.Helper()
	db := EmptyDB(tb)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	types, err := parser.ParseSchemaString(kitchenSinkSchemaFGA)
	require.NoError(tb, err, "parse kitchen-sink schema")

	m := migrator.NewMigrator(db, "")
	require.NoError(tb, m.MigrateWithTypes(ctx, types), "migrate kitchen-sink schema")

	_, err = db.ExecContext(ctx, `
		CREATE TABLE kitchen_sink_tuples (
			subject_type TEXT NOT NULL,
			subject_id   TEXT NOT NULL,
			relation     TEXT NOT NULL,
			object_type  TEXT NOT NULL,
			object_id    TEXT NOT NULL
		);
		CREATE INDEX ks_by_object  ON kitchen_sink_tuples (object_type, object_id, relation);
		CREATE INDEX ks_by_subject ON kitchen_sink_tuples (subject_type, subject_id, relation);
		CREATE INDEX ks_by_edge    ON kitchen_sink_tuples (object_type, relation, subject_type, subject_id);
		CREATE VIEW melange_tuples AS
			SELECT subject_type, subject_id, relation, object_type, object_id FROM kitchen_sink_tuples;
	`)
	require.NoError(tb, err, "create kitchen_sink_tuples table + melange_tuples view")
	return db
}

// LoadKitchenSinkTuples bulk-loads tuples via COPY (batched INSERT fallback).
func LoadKitchenSinkTuples(tb testing.TB, db *sql.DB, tuples []KitchenSinkTuple) {
	tb.Helper()
	ctx := context.Background()
	bf := NewBulkFixtures(ctx, db)

	var buf strings.Builder
	for _, tp := range tuples {
		buf.WriteString(tp.SubjectType)
		buf.WriteByte('\t')
		buf.WriteString(tp.SubjectID)
		buf.WriteByte('\t')
		buf.WriteString(tp.Relation)
		buf.WriteByte('\t')
		buf.WriteString(tp.ObjectType)
		buf.WriteByte('\t')
		buf.WriteString(tp.ObjectID)
		buf.WriteByte('\n')
	}
	cols := []string{"subject_type", "subject_id", "relation", "object_type", "object_id"}
	if err := bf.copyFrom("kitchen_sink_tuples", cols, strings.NewReader(buf.String())); err != nil {
		// Fallback: batched INSERT.
		require.NoError(tb, insertKitchenSinkTuplesBatched(ctx, db, tuples), "load kitchen-sink tuples")
	}
	_, err := db.ExecContext(ctx, "ANALYZE kitchen_sink_tuples")
	require.NoError(tb, err)
}

func insertKitchenSinkTuplesBatched(ctx context.Context, db *sql.DB, tuples []KitchenSinkTuple) error {
	const batch = 1000
	for i := 0; i < len(tuples); i += batch {
		end := min(i+batch, len(tuples))
		vals := make([]string, 0, end-i)
		args := make([]any, 0, 5*(end-i))
		for j, tp := range tuples[i:end] {
			b := j * 5
			vals = append(vals, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", b+1, b+2, b+3, b+4, b+5))
			args = append(args, tp.SubjectType, tp.SubjectID, tp.Relation, tp.ObjectType, tp.ObjectID)
		}
		q := "INSERT INTO kitchen_sink_tuples (subject_type,subject_id,relation,object_type,object_id) VALUES " + strings.Join(vals, ",")
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return nil
}
