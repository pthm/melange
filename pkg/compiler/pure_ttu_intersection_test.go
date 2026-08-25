package compiler_test

import (
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/stretchr/testify/require"
)

func TestGenerateSQL_PureTTUIntersection(t *testing.T) {
	model := `
model
  schema 1.1

type user

type project
  relations
    define reader: [user]

type document
  relations
    define primary_project: [project]
    define secondary_project: [project]
    define can_view: reader from primary_project and reader from secondary_project
`

	types, err := parser.ParseSchemaString(model)
	require.NoError(t, err)

	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inlineData := compiler.BuildInlineSQLData(closureRows, analyses)

	generated, err := compiler.GenerateSQL(analyses, inlineData, "")
	require.NoError(t, err)
	require.Contains(t, compiler.CollectFunctionNames(analyses), "check_document_can_view")
	require.Contains(t, generated.Dispatcher, "check_document_can_view")

	var canViewFunction string
	for _, function := range generated.Functions {
		if strings.Contains(function, "CREATE OR REPLACE FUNCTION check_document_can_view(") {
			canViewFunction = function
			break
		}
	}
	require.NotEmpty(t, canViewFunction, "generated SQL should contain check_document_can_view")
	require.Contains(t, canViewFunction, "primary_project")
	require.Contains(t, canViewFunction, "secondary_project")
}
