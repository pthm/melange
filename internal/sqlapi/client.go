package sqlapi

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// The client comparison: every name a generated client emits (relation
// constants and accessible function names), checked against a database's
// deployed report. A client narrower than the database is always fine; a
// name the database does not answer for is reported with why and how to fix
// it.

// ClientNames are the names a generated client emits: the relations it
// selects, in model order, and the accessible functions it names.
type ClientNames struct {
	Pairs     []clientgen.RelationRef
	Functions []sqlgen.AccessibleFunction
	// Types is the model the client is generated from, and SchemaContent
	// the schema text it was parsed from. A caller comparing with a database
	// that records no model describes the database with Types, and predicts
	// what migrate does with the client's configuration from both.
	Types         []schema.TypeDefinition
	SchemaContent string
}

// NewClientNames returns the names a client resolved as m emits, with the
// model it was resolved from and that model's schema text (nil and "" when
// the caller has none).
func NewClientNames(m *clientgen.Model, types []schema.TypeDefinition, schemaContent string) ClientNames {
	return ClientNames{
		Pairs:         m.Selected,
		Functions:     slices.Concat(m.Accessible, m.AccessibleByParent),
		Types:         types,
		SchemaContent: schemaContent,
	}
}

// ClientSeverity is how much a client finding matters.
type ClientSeverity string

const (
	// SeverityError: the client names something the database does not
	// answer for, so the call fails.
	SeverityError ClientSeverity = "error"
	// SeverityWarning: the database answers today, but a later migration
	// or the application's own objects may change that.
	SeverityWarning ClientSeverity = "warning"
	// SeverityInfo: a difference that breaks nothing; not counted.
	SeverityInfo ClientSeverity = "info"
)

// Remedy is what a finding's fix does, for a caller that words fixes with
// what it knows about the next migrate (doctor).
type Remedy string

const (
	// RemedyMigrate: a migrate with melange.yaml's schema and sql section
	// applies it.
	RemedyMigrate Remedy = "migrate"
	// RemedyReinstall: the last migration installed the function and it is
	// gone from the database.
	RemedyReinstall Remedy = "reinstall"
	// RemedyRegenerate: the client names more than melange.yaml installs.
	RemedyRegenerate Remedy = "regenerate"
)

// ClientFinding is one name of the client that the database does not answer
// for as the client expects.
type ClientFinding struct {
	Code     string         `json:"code"`
	Severity ClientSeverity `json:"severity"`
	Type     string         `json:"type,omitempty"`
	Relation string         `json:"relation,omitempty"`
	Link     string         `json:"link,omitempty"`
	Function string         `json:"function,omitempty"`
	Message  string         `json:"message"`
	Fix      string         `json:"fix,omitempty"`
	Remedy   Remedy         `json:"-"`
}

// ClientReport is the result of CompareClient.
type ClientReport struct {
	Findings         []ClientFinding `json:"findings"`
	Errors           int             `json:"errors"`
	Warnings         int             `json:"warnings"`
	FunctionsChecked int             `json:"functions_checked"`
	PairsChecked     int             `json:"relations_checked"`
	Notes            []string        `json:"notes,omitempty"`
}

func (cr *ClientReport) add(f ClientFinding) {
	cr.Findings = append(cr.Findings, f)
	switch f.Severity {
	case SeverityError:
		cr.Errors++
	case SeverityWarning:
		cr.Warnings++
	}
}

// CompareClient checks the names a client emits against r, a checked report
// of the database (Deployed), and rec, its last migration record or nil.
// local is the exposure the client was generated under (melange.yaml's sql
// section), which says whether a migrate would install what is missing.
func CompareClient(r *Report, rec *migrator.MigrationRecord, names ClientNames, local sqlgen.Exposure) *ClientReport {
	cr := &ClientReport{
		Findings:         []ClientFinding{},
		FunctionsChecked: len(names.Functions),
		PairsChecked:     len(names.Pairs),
		Notes:            slices.Clone(r.Notes),
	}
	type owned struct {
		fn  Function
		rel *Relation
	}
	byName := make(map[string][]owned)
	for i := range r.Relations {
		rel := &r.Relations[i]
		for _, f := range rel.Functions {
			byName[f.Name] = append(byName[f.Name], owned{f, rel})
		}
	}
	orphans, unmanaged := make(map[string]bool), make(map[string]bool)
	for _, f := range r.Orphans {
		orphans[f.Name] = true
	}
	for _, f := range r.Unmanaged {
		unmanaged[f.Name] = true
	}
	unrecordedInstalled := false

	for _, f := range names.Functions {
		kind := sqlgen.KindAccessible
		if f.Link != "" {
			kind = sqlgen.KindParentKey
		}
		finding := ClientFinding{Type: f.ObjectType, Relation: f.Relation, Link: f.Link, Function: f.Name}
		owners := byName[f.Name]
		var match *owned
		for i := range owners {
			o := &owners[i]
			if o.fn.Kind == kind && o.fn.Type == f.ObjectType && o.fn.Relation == f.Relation && o.fn.Link == f.Link {
				match = o
				break
			}
		}
		switch {
		case len(owners) > 1:
			// CREATE OR REPLACE keeps one definition for the name, which may
			// answer for another relation than the client means.
			descs := make([]string, len(owners))
			for i, o := range owners {
				descs[i] = describeOwner(o.fn)
			}
			finding.Code, finding.Severity = "function_collision", SeverityError
			finding.Message = fmt.Sprintf("%s is generated for %s; the database keeps only one of them, so a call may answer for another relation",
				f.Name, strings.Join(descs, " and "))
			finding.Fix = "rename one of the relations so their function names differ"
		case match != nil && match.fn.Status == StatusMissing && rec == nil:
			// No migration installed it; the usual cause is migration files
			// generated under another exposure or schema.
			finding.Code, finding.Severity = "function_not_installed", SeverityError
			finding.Message = fmt.Sprintf("%s is not installed in schema %q; no migration is recorded, so it is compared with what melange.yaml generates", f.Name, r.DatabaseSchema)
			finding.Fix, finding.Remedy = clientFix(r, local, f)
		case match != nil && match.fn.Status == StatusMissing:
			finding.Code, finding.Severity, finding.Remedy = "function_not_installed", SeverityError, RemedyReinstall
			finding.Message = fmt.Sprintf("%s is not installed: the database's last migration installs it, and it is missing from schema %q", f.Name, r.DatabaseSchema)
			finding.Fix = r.reinstall()
		case match != nil && match.fn.Status == StatusSignatureDiffers:
			finding.Code, finding.Severity = "function_signature", SeverityError
			finding.Message = fmt.Sprintf("%s is installed with other arguments than this melange generates", f.Name)
			finding.Fix = match.fn.Detail
		case match != nil && !match.fn.Public:
			finding.Code, finding.Severity = "function_internal", SeverityWarning
			finding.Message = fmt.Sprintf("%s is installed only as an internal function: %s, so a later migration may drop it or change its signature",
				f.Name, internalReason(r.Exposure, describeRef(f)))
			finding.Fix, finding.Remedy = clientFix(r, local, f)
		case match != nil:
			continue
		case len(owners) > 0:
			o := owners[0]
			finding.Code, finding.Severity = "function_owner", SeverityError
			finding.Message = fmt.Sprintf("%s belongs to %s in the database's SQL API, not to %s: a name collision, or a name another melange version computes differently",
				f.Name, describeOwner(o.fn), describeRef(f))
			finding.Fix, finding.Remedy = "regenerate the client with the melange version that migrated the database", RemedyRegenerate
			if r.CodegenAhead && o.fn.Status != StatusMissing && recordedName(rec, f.Name) {
				finding.Severity = SeverityInfo
				finding.Message += "; it is installed and recorded by the newer melange that migrated the database"
			}
		case orphans[f.Name]:
			finding.Code, finding.Severity = "function_orphan", SeverityWarning
			finding.Message = fmt.Sprintf("%s is installed, but this melange does not generate it for the deployed model, so the next melange migrate drops it", f.Name)
			finding.Fix, finding.Remedy = clientFix(r, local, f)
			if r.CodegenAhead {
				finding.Severity = SeverityInfo
				finding.Message = fmt.Sprintf("%s is installed and recorded by the newer melange that migrated the database; this melange does not generate it for the deployed model", f.Name)
				finding.Fix, finding.Remedy = "", ""
			}
		case unmanaged[f.Name] && rec == nil:
			// Without a record nothing tells an application's function
			// from melange's; the name is installed.
			unrecordedInstalled = true
			continue
		case unmanaged[f.Name]:
			finding.Code, finding.Severity = "function_unmanaged", SeverityWarning
			finding.Message = fmt.Sprintf("%s is installed, but no migration recorded it: an application's own function has the name, and melange does not install it for the deployed model", f.Name)
			finding.Fix, finding.Remedy = clientFix(r, local, f)
		default:
			finding.Code, finding.Severity = "function_not_installed", SeverityError
			finding.Message = fmt.Sprintf("%s is not installed: %s", f.Name, notInstalledReason(r, f, kind))
			finding.Fix, finding.Remedy = clientFix(r, local, f)
		}
		cr.add(finding)
	}
	if unrecordedInstalled {
		cr.Notes = append(cr.Notes, "No migration is recorded, so accessible functions installed under the names the client uses are taken as melange's.")
	}

	for _, p := range names.Pairs {
		rel := r.Find(p.Type, p.Relation)
		ref := p.Type + "#" + p.Relation
		finding := ClientFinding{Type: p.Type, Relation: p.Relation}
		switch {
		case rel == nil:
			finding.Code, finding.Severity, finding.Remedy = "relation_undefined", SeverityWarning, RemedyMigrate
			finding.Message = fmt.Sprintf("the deployed model has no %s, so check_permission denies it", ref)
			finding.Fix = r.migrateFix("applies the local schema")
		case rel.Exposed:
			continue
		case local.Exposes(p.Type, p.Relation):
			finding.Code, finding.Severity, finding.Remedy = "relation_not_exposed", SeverityError, RemedyMigrate
			finding.Message = fmt.Sprintf("melange.yaml exposes %s, but the database was migrated without it, so check_permission raises %s for it", ref, sqlgen.NotExposedErrCode)
			finding.Fix = r.migrateFix("exposes it as melange.yaml asks")
		default:
			finding.Code, finding.Severity = "relation_internal", SeverityInfo
			finding.Message = fmt.Sprintf("the client has a constant for %s, which neither melange.yaml nor the database exposes; check_permission raises %s for it (constants used for tuples are fine)", ref, sqlgen.NotExposedErrCode)
		}
		cr.add(finding)
	}

	for _, fam := range []struct {
		key   string
		local *bool
		db    *bool
	}{
		{"list_objects", local.Functions.ListObjects, r.Exposure.Functions.ListObjects},
		{"list_subjects", local.Functions.ListSubjects, r.Exposure.Functions.ListSubjects},
		{"explain", local.Functions.Explain, r.Exposure.Functions.Explain},
		{"expand", local.Functions.Expand, r.Exposure.Functions.Expand},
	} {
		if on(fam.local) && !on(fam.db) {
			cr.add(ClientFinding{
				Code: "family_off", Severity: SeverityInfo,
				Message: fmt.Sprintf("melange.yaml turns sql.functions.%s on, and the database was migrated with it off; the client names none of its functions", fam.key),
			})
		}
	}
	if r.CodegenSkew {
		cr.Notes = append(cr.Notes, fmt.Sprintf("The client's names are the ones this melange (%s) generates; the database's functions were generated by melange %s.",
			displayVersion(r.MelangeVersion), displayVersion(r.DeployedCodegenVersion)))
	}
	return cr
}

// on reports whether a family flag is on (nil means on).
func on(flag *bool) bool { return flag == nil || *flag }

// recordedName reports whether the last migration recorded the function name.
func recordedName(rec *migrator.MigrationRecord, name string) bool {
	return rec != nil && slices.Contains(rec.FunctionNames, name)
}

// localInstalls reports whether a migration under local installs f as
// public API: the relation exposed, with its family on.
func localInstalls(local sqlgen.Exposure, f sqlgen.AccessibleFunction) bool {
	if !local.Exposes(f.ObjectType, f.Relation) {
		return false
	}
	if f.Link != "" {
		return on(local.Functions.ParentKey)
	}
	return on(local.Functions.Accessible)
}

// clientFix is the fix for a client function the database does not install
// as public API: migrate when melange.yaml installs it, else regenerate the
// client, which then no longer names it.
func clientFix(r *Report, local sqlgen.Exposure, f sqlgen.AccessibleFunction) (string, Remedy) {
	if localInstalls(local, f) {
		return r.migrateFix("installs it as melange.yaml asks"), RemedyMigrate
	}
	return "regenerate the client: melange.yaml does not install it", RemedyRegenerate
}

// migrateFix says how to apply melange.yaml; what is what that does.
func (r *Report) migrateFix(what string) string {
	if r.CodegenAhead {
		v := displayVersion(r.DeployedCodegenVersion)
		return fmt.Sprintf("a newer melange (%s) migrated the database and this melange's migrate refuses to downgrade it: use melange %s or later and run melange migrate with it", v, v)
	}
	return "run melange migrate, which " + what
}

// notInstalledReason says why the database has no function f of kind.
func notInstalledReason(r *Report, f sqlgen.AccessibleFunction, kind sqlgen.FunctionKind) string {
	ref := f.ObjectType + "#" + f.Relation
	rel := r.Find(f.ObjectType, f.Relation)
	if rel == nil {
		return "the deployed model has no " + ref
	}
	for _, ni := range rel.NotInstalled {
		if ni.Kind != kind {
			continue
		}
		switch ni.Reason {
		case sqlgen.ReasonFamilyOff:
			if kind == sqlgen.KindParentKey {
				return "the database was migrated with sql.functions.parent_key off"
			}
			return "the database was migrated with sql.functions.list_objects, accessible and parent_key off"
		case sqlgen.ReasonNotExposed:
			return "the database does not expose " + ref
		}
	}
	return "this melange generates none for " + describeRef(f) + " under the database's model"
}

// internalReason says why the database installs an accessible function the
// client names only as an internal one.
func internalReason(e sqlgen.Exposure, what string) string {
	if !on(e.Functions.Accessible) {
		return "the database was migrated with sql.functions.accessible off, and the list functions use it"
	}
	return fmt.Sprintf("the database does not expose %s, and an exposed relation resolves through it", what)
}

// describeRef names the relation an accessible function is for.
func describeRef(f sqlgen.AccessibleFunction) string {
	if f.Link == "" {
		return f.ObjectType + "#" + f.Relation
	}
	return fmt.Sprintf("%s#%s by %s", f.ObjectType, f.Relation, f.Link)
}

// describeOwner names the relation a catalog function is for, with its kind.
func describeOwner(f Function) string {
	if f.Link != "" {
		return fmt.Sprintf("%s#%s by %s (%s)", f.Type, f.Relation, f.Link, f.Kind)
	}
	return fmt.Sprintf("%s#%s (%s)", f.Type, f.Relation, f.Kind)
}
