package sqlgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/stephenafamo/bob"
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
