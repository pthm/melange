package command

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// readDeployedModel opens dsn and returns the model recorded by the most recent
// migration. Shared by `schema pull` and `diff` so their handling — including
// the "never migrated" vs "migrated before the model was recorded" distinction
// and connection errors — stays consistent.
func readDeployedModel(dsn, databaseSchema string) (*migrator.DeployedModel, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)

	model, err := m.GetDeployedModel(ctx)
	if err != nil {
		return nil, cli.GeneralError("reading deployed model", err)
	}
	if model != nil {
		return model, nil
	}

	// No model recorded: distinguish an un-migrated database from one migrated
	// before model storage. A failed re-fetch is surfaced rather than reported
	// as "never migrated".
	rec, rerr := m.GetLastMigration(ctx)
	switch {
	case rerr != nil:
		return nil, cli.GeneralError("reading migration record", rerr)
	case rec != nil:
		return nil, cli.GeneralError("this database was migrated before melange v0.9, so the model was not recorded — re-run `melange migrate` with a current version to record it", nil)
	default:
		return nil, cli.GeneralError("no melange migration found in this database", nil)
	}
}

// deployedTypesFromRecord returns the parsed model stored in a migration row,
// preferring model_json and falling back to parsing the recorded DSL when the
// JSON is absent or empty — the same order `doctor` uses, so both agree on what
// a database has deployed.
//
// Returns nil types and nil error when the row predates model storage (no DSL
// recorded), which callers report as "not recorded" rather than as an error.
func deployedTypesFromRecord(rec *migrator.MigrationRecord) ([]schema.TypeDefinition, error) {
	if rec == nil || rec.SchemaDSL == "" {
		return nil, nil
	}
	if types, err := schema.UnmarshalModel(rec.ModelJSON); err == nil && len(types) > 0 {
		return types, nil
	}
	types, err := parser.ParseSchemaString(rec.SchemaDSL)
	if err != nil {
		return nil, fmt.Errorf("parsing recorded schema DSL: %w", err)
	}
	return types, nil
}
