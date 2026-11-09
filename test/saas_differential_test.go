package test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/test/testutil"
)

// saasProbes are the (object type, relation) pairs of the SaaS model the
// differential test covers: the area readers (intersections over the partner
// and support routes), the capabilities the benchmark measures, and the
// resources granting through their org.
var saasProbes = []struct{ objType, relation string }{
	{"organization", "can_read"},
	{"organization", "can_switch_to"},
	{"organization", "can_read_settings"},
	{"organization", "contact_reader"},
	{"organization", "can_read_contact_pii"},
	{"organization", "can_edit_branding"},
	{"organization", "can_manage_clients"},
	{"organization", "can_export"},
	{"partner", "participant"},
	{"contact", "can_read"},
	{"contact", "can_read_pii"},
	{"deal", "can_edit"},
	{"deal", "can_close"},
	{"incident", "can_comment"},
	{"incident", "can_be_mentioned"},
	{"service_request", "can_resolve"},
	{"invoice", "can_approve"},
	{"employee", "can_read"},
	{"folder", "can_read"},
	{"folder", "can_edit"},
	{"document", "can_read"},
	{"document", "can_edit"},
}

// TestSaaS_ListVsCheck checks list_objects, the accessible functions and
// list_subjects against check_permission over the small SaaS dataset, for
// members, partner users and support staff, and checks that filtering
// contacts by their org through the parent-key function returns the contacts
// list_objects returns. The benchmark measures these functions on the same
// model and view, so this is what keeps it from measuring wrong answers.
func TestSaaS_ListVsCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("SaaS differential test requires a database")
	}
	d := testutil.SetupSaaSDB(t, testutil.SaaSScaleSmall)
	db := d.DB

	users := distinct(t, db, `SELECT id FROM users WHERE active AND NOT is_staff ORDER BY id`)
	subjects := slices.Concat(sample(users, 8), []string{d.Staff, d.BigMember, d.PartnerUser, d.SmallMember})
	everyone := slices.Concat(users, []string{d.Staff})

	var totalAllow, totalDeny int
	for _, p := range saasProbes {
		t.Run(p.objType+"."+p.relation, func(t *testing.T) {
			objects := distinct(t, db, `SELECT DISTINCT object_id FROM melange_tuples WHERE object_type = $1 ORDER BY object_id`, p.objType)
			require.NotEmpty(t, objects, "no objects of type %s", p.objType)

			for _, u := range subjects {
				listed := setOf(listObjects(t, db, "user", u, p.relation, p.objType))
				requireAccessibleMatchesList(t, db, u, p.relation, p.objType, listed)
				require.Equalf(t, setToSlice(listed), pagedObjects(t, db, "user", u, p.relation, p.objType, 7),
					"pages of list_objects(%s.%s) for user:%s", p.objType, p.relation, u)
				for _, o := range objects {
					allowed := checkPerm(t, db, "user", u, p.relation, p.objType, o) == 1
					if allowed {
						totalAllow++
					} else {
						totalDeny++
					}
					_, inList := listed[o]
					require.Equalf(t, allowed, inList, "list_objects vs check for user:%s %s %s:%s", u, p.relation, p.objType, o)
				}
			}

			for _, o := range sample(objects, 6) {
				listed := setOf(listSubjects(t, db, p.objType, o, p.relation, "user"))
				for _, u := range everyone {
					allowed := checkPerm(t, db, "user", u, p.relation, p.objType, o) == 1
					_, inList := listed[u]
					require.Equalf(t, allowed, inList, "list_subjects vs check for user:%s %s %s:%s", u, p.relation, p.objType, o)
				}
			}
		})
	}
	t.Logf("coverage: %d allows, %d denies across %d probes", totalAllow, totalDeny, len(saasProbes))
	require.Greater(t, totalAllow, 500, "too few granted pairs")
	require.Greater(t, totalDeny, 500, "too few denied pairs")

	// Every viewer the benchmark uses reads the largest org, by its own route.
	for _, u := range []string{d.Staff, d.BigMember, d.PartnerUser} {
		require.Equal(t, 1, checkPerm(t, db, "user", u, "contact_reader", "organization", d.BigOrg), "user:%s contact_reader on the largest org", u)
	}

	// A team userset subject: what list_objects returns, check grants.
	team := distinct(t, db, `SELECT team_id || '#member' FROM folder_shares WHERE team_id IS NOT NULL ORDER BY 1 LIMIT 1`)
	require.NotEmpty(t, team)
	listed := listObjects(t, db, "team", team[0], "can_read", "folder")
	require.NotEmpty(t, listed, "team:%s lists no folders", team[0])
	for _, o := range listed {
		require.Equal(t, 1, checkPerm(t, db, "team", team[0], "can_read", "folder", o), "list_objects over-reported team:%s on folder:%s", team[0], o)
	}

	t.Run("parent key", func(t *testing.T) {
		for _, u := range subjects {
			byOrg := distinct(t, db, `SELECT id FROM contacts
				WHERE org_id IN (SELECT * FROM accessible_contact_can_read_by_org('user', $1)) ORDER BY id`, u)
			require.ElementsMatch(t, listObjects(t, db, "user", u, "can_read", "contact"), byOrg, "contacts by org for user:%s", u)
		}
	})
}
