package parser

import (
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
)

// Conditions are unsupported and dropped by convertModel, so parsing must
// fail closed rather than report a narrower schema as valid (issue #81).
func TestParseSchemaString_RejectsConditions(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name: "applied to a type restriction",
			schema: `model
  schema 1.1

type user

type document
  relations
    define viewer: [user with non_expired_grant]

condition non_expired_grant(current_time: timestamp, grant_time: timestamp) {
  current_time < grant_time
}`,
			want: "document#viewer allows [user with non_expired_grant]",
		},
		{
			name: "applied to a userset restriction",
			schema: `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type document
  relations
    define viewer: [group#member with non_expired_grant]

condition non_expired_grant(current_time: timestamp, grant_time: timestamp) {
  current_time < grant_time
}`,
			want: "document#viewer allows [group#member with non_expired_grant]",
		},
		{
			name: "declared but never applied",
			schema: `model
  schema 1.1

type user

type document
  relations
    define viewer: [user]

condition non_expired_grant(current_time: timestamp, grant_time: timestamp) {
  current_time < grant_time
}`,
			want: `condition "non_expired_grant" is declared`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSchemaString(tt.schema)
			if err == nil {
				t.Fatal("expected conditions to be rejected, got nil error")
			}
			if !melange.IsInvalidSchemaErr(err) {
				t.Errorf("expected an ErrInvalidSchema, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestParseSchemaString_AllowsConditionFreeSchema(t *testing.T) {
	_, err := ParseSchemaString(`model
  schema 1.1

type user

type document
  relations
    define viewer: [user, user:*]`)
	if err != nil {
		t.Fatalf("condition-free schema should parse: %v", err)
	}
}
