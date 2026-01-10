package sqlgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql/dialect"
)

func renderQuery(q bob.Query) (string, error) {
	sql, args, err := bob.Build(context.Background(), q)
	if err != nil {
		return "", err
	}
	if len(args) != 0 {
		return "", fmt.Errorf("unexpected query args: %d", len(args))
	}
	return strings.TrimSpace(sql), nil
}

func RenderQuery(q bob.Query) (string, error) {
	return renderQuery(q)
}

func existsSQL(q bob.Query) (string, error) {
	sql, err := renderQuery(q)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("EXISTS (\n%s\n)", sql), nil
}

// RenderExpr renders a bob.Expression to SQL string
func RenderExpr(expr bob.Expression) (string, error) {
	var buf strings.Builder
	args, err := bob.Express(context.Background(), &buf, dialect.Dialect, 1, expr)
	if err != nil {
		return "", err
	}
	if len(args) != 0 {
		return "", fmt.Errorf("unexpected expression args: %d", len(args))
	}
	return strings.TrimSpace(buf.String()), nil
}

// RenderExprs renders a slice of bob.Expression to SQL strings
func RenderExprs(exprs []bob.Expression) ([]string, error) {
	result := make([]string, 0, len(exprs))
	for _, expr := range exprs {
		sql, err := RenderExpr(expr)
		if err != nil {
			return nil, err
		}
		result = append(result, sql)
	}
	return result, nil
}
