package doctor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is a representative sample of what pg_get_viewdef('melange_tuples'::regclass, true)
// returns for a typical melange_tuples view.
const testViewSQL = ` SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'organization'::text AS object_type,
    organization_id::text AS object_id
   FROM organization_members
UNION ALL
 SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'team'::text AS object_type,
    team_id::text AS object_id
   FROM team_members
UNION ALL
 SELECT 'organization'::text AS subject_type,
    organization_id::text AS subject_id,
    'org'::text AS relation,
    'repository'::text AS object_type,
    id::text AS object_id
   FROM repositories
  WHERE (organization_id IS NOT NULL)
UNION ALL
 SELECT 'user'::text AS subject_type,
    CASE
        WHEN banned_all THEN '*'::text
        ELSE user_id::text
    END AS subject_id,
    'banned'::text AS relation,
    'repository'::text AS object_type,
    repository_id::text AS object_id
   FROM repository_bans;`

func TestParseViewSQL_Basic(t *testing.T) {
	vd, err := parseViewSQL(testViewSQL)
	require.NoError(t, err)

	assert.False(t, vd.HasUnion, "should not detect bare UNION")
	assert.Len(t, vd.Branches, 4)

	// First branch: organization_members
	b0 := vd.Branches[0]
	require.Len(t, b0.SourceTables, 1)
	assert.Equal(t, "organization_members", b0.SourceTables[0].Name)
	assert.Equal(t, "user_id::text", b0.ColumnMapping["subject_id"])
	assert.Equal(t, "organization_id::text", b0.ColumnMapping["object_id"])
	assert.Equal(t, "role", b0.ColumnMapping["relation"])
	assert.Equal(t, "'organization'::text", b0.ColumnMapping["object_type"])

	// Check cast columns
	assert.Len(t, b0.CastColumns, 2)
	castCols := map[string]string{}
	for _, cc := range b0.CastColumns {
		castCols[cc.ViewColumn] = cc.SourceColumn
	}
	assert.Equal(t, "user_id", castCols["subject_id"])
	assert.Equal(t, "organization_id", castCols["object_id"])
}

func TestParseViewSQL_CaseWhen(t *testing.T) {
	vd, err := parseViewSQL(testViewSQL)
	require.NoError(t, err)

	// Last branch: repository_bans with CASE WHEN
	last := vd.Branches[3]
	require.Len(t, last.SourceTables, 1)
	assert.Equal(t, "repository_bans", last.SourceTables[0].Name)

	// CASE WHEN should still detect the cast on user_id
	found := false
	for _, cc := range last.CastColumns {
		if cc.ViewColumn == "subject_id" && cc.SourceColumn == "user_id" {
			found = true
		}
	}
	assert.True(t, found, "should detect user_id::text cast inside CASE WHEN")

	// repository_id::text should be detected too
	found = false
	for _, cc := range last.CastColumns {
		if cc.ViewColumn == "object_id" && cc.SourceColumn == "repository_id" {
			found = true
		}
	}
	assert.True(t, found, "should detect repository_id::text cast")
}

func TestParseViewSQL_BareUnion(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id
   FROM table_a
UNION
 SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id
   FROM table_b;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	assert.True(t, vd.HasUnion, "should detect bare UNION")
	assert.Len(t, vd.Branches, 2)
}

func TestParseViewSQL_SchemaQualified(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    'member' AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM myschema.org_members;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 1)
	assert.Equal(t, "myschema", vd.Branches[0].SourceTables[0].Schema)
	assert.Equal(t, "org_members", vd.Branches[0].SourceTables[0].Name)
}

func TestParseViewSQL_WhereClause(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'repo'::text AS object_type,
    id::text AS object_id
   FROM repositories
  WHERE (organization_id IS NOT NULL)
    AND (active = true);`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)
	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 1)
	assert.Equal(t, "repositories", vd.Branches[0].SourceTables[0].Name)
}

func TestParseViewSQL_Empty(t *testing.T) {
	_, err := parseViewSQL("")
	assert.Error(t, err)

	_, err = parseViewSQL("   ")
	assert.Error(t, err)
}

func TestParseViewSQL_SingleBranch(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM org_members;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	assert.False(t, vd.HasUnion)
	assert.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 1)
	assert.Equal(t, "org_members", vd.Branches[0].SourceTables[0].Name)
}

func TestParseViewSQL_NoCasts(t *testing.T) {
	sql := `SELECT subject_type,
    subject_id,
    relation,
    object_type,
    object_id
   FROM some_table;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	assert.Empty(t, vd.Branches[0].CastColumns, "should have no cast columns")
}

func TestParseViewSQL_MultiTableFrom(t *testing.T) {
	// Reproduces https://github.com/pthm/melange/issues/35
	// pg_get_viewdef formats comma-joins across multiple lines.
	sql := ` SELECT 'site'::text AS object_type,
    (sites.id)::text AS object_id,
    'organization'::text AS relation,
    'org'::text AS subject_type,
    organization.org_id AS subject_id
   FROM sites,
    organization
UNION ALL
 SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'team'::text AS object_type,
    team_id::text AS object_id
   FROM team_members;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 2)

	// First branch should have both tables from the cross join
	b0 := vd.Branches[0]
	require.Len(t, b0.SourceTables, 2)
	assert.Equal(t, "sites", b0.SourceTables[0].Name)
	assert.Equal(t, "organization", b0.SourceTables[1].Name)

	// Second branch has a single table
	b1 := vd.Branches[1]
	require.Len(t, b1.SourceTables, 1)
	assert.Equal(t, "team_members", b1.SourceTables[0].Name)
}

func TestParseViewSQL_MultiTableFromSingleLine(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    'member' AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM users, organizations;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 2)
	assert.Equal(t, "users", vd.Branches[0].SourceTables[0].Name)
	assert.Equal(t, "organizations", vd.Branches[0].SourceTables[1].Name)
}

func TestParseViewSQL_ExplicitJoin(t *testing.T) {
	// pg_get_viewdef formats explicit JOINs across multiple lines.
	sql := ` SELECT 'site'::text AS object_type,
    (sites.id)::text AS object_id,
    'organization'::text AS relation,
    'org'::text AS subject_type,
    organization.org_id AS subject_id
   FROM sites
     JOIN organization ON sites.org_id = organization.id
UNION ALL
 SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'team'::text AS object_type,
    team_id::text AS object_id
   FROM team_members;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 2)

	b0 := vd.Branches[0]
	require.Len(t, b0.SourceTables, 2, "should find both tables from explicit JOIN")
	assert.Equal(t, "sites", b0.SourceTables[0].Name)
	assert.Equal(t, "organization", b0.SourceTables[1].Name)

	b1 := vd.Branches[1]
	require.Len(t, b1.SourceTables, 1)
	assert.Equal(t, "team_members", b1.SourceTables[0].Name)
}

func TestParseViewSQL_LeftJoin(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    'member' AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM users
     LEFT JOIN organizations ON users.org_id = organizations.id;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 2, "should find both tables from LEFT JOIN")
	assert.Equal(t, "users", vd.Branches[0].SourceTables[0].Name)
	assert.Equal(t, "organizations", vd.Branches[0].SourceTables[1].Name)
}

func TestParseViewSQL_OnlyKeyword(t *testing.T) {
	// PostgreSQL's ONLY keyword excludes child tables in inheritance.
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM ONLY org_members;`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 1)
	assert.Equal(t, "org_members", vd.Branches[0].SourceTables[0].Name,
		"should skip ONLY keyword and extract table name")
}

func TestParseViewSQL_QuotedIdentifier(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM "OrgMembers";`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 1)
	assert.Equal(t, "OrgMembers", vd.Branches[0].SourceTables[0].Name,
		"should strip quotes from identifier")
}

func TestParseViewSQL_QuotedSchemaQualified(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    'member' AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM "MySchema"."OrgMembers";`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 1)
	assert.Equal(t, "MySchema", vd.Branches[0].SourceTables[0].Schema,
		"should strip quotes from schema")
	assert.Equal(t, "OrgMembers", vd.Branches[0].SourceTables[0].Name,
		"should strip quotes from table name")
}

func TestParseViewSQL_JoinWithUsing(t *testing.T) {
	sql := `SELECT 'user'::text AS subject_type,
    user_id::text AS subject_id,
    'member' AS relation,
    'org'::text AS object_type,
    org_id::text AS object_id
   FROM users
     JOIN organizations USING (org_id);`

	vd, err := parseViewSQL(sql)
	require.NoError(t, err)

	require.Len(t, vd.Branches, 1)
	require.Len(t, vd.Branches[0].SourceTables, 2)
	assert.Equal(t, "users", vd.Branches[0].SourceTables[0].Name)
	assert.Equal(t, "organizations", vd.Branches[0].SourceTables[1].Name)
}

func TestTableRef_String(t *testing.T) {
	assert.Equal(t, "users", TableRef{Name: "users"}.String())
	assert.Equal(t, "public.users", TableRef{Schema: "public", Name: "users"}.String())
}

func TestHasBareUnion(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		expected bool
	}{
		{"union all only", "SELECT 1 UNION ALL SELECT 2", false},
		{"bare union", "SELECT 1 UNION SELECT 2", true},
		{"mixed", "SELECT 1 UNION ALL SELECT 2 UNION SELECT 3", true},
		{"no union", "SELECT 1 FROM t", false},
		{"union in string", "SELECT 'UNION' FROM t", true}, // known limitation: string content not distinguished
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, hasBareUnion(tt.sql))
		})
	}
}
