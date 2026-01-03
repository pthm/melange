package melange_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/pthm/melange"
)

func TestErrorHelpers(t *testing.T) {
	t.Run("IsNoTuplesTableErr", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", melange.ErrNoTuplesTable)
		if !melange.IsNoTuplesTableErr(err) {
			t.Error("IsNoTuplesTableErr should return true for wrapped ErrNoTuplesTable")
		}
		if melange.IsNoTuplesTableErr(errors.New("other error")) {
			t.Error("IsNoTuplesTableErr should return false for other errors")
		}
	})

	t.Run("IsMissingModelErr", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", melange.ErrMissingModel)
		if !melange.IsMissingModelErr(err) {
			t.Error("IsMissingModelErr should return true for wrapped ErrMissingModel")
		}
		if melange.IsMissingModelErr(errors.New("other error")) {
			t.Error("IsMissingModelErr should return false for other errors")
		}
	})

	t.Run("IsEmptyModelErr", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", melange.ErrEmptyModel)
		if !melange.IsEmptyModelErr(err) {
			t.Error("IsEmptyModelErr should return true for wrapped ErrEmptyModel")
		}
		if melange.IsEmptyModelErr(errors.New("other error")) {
			t.Error("IsEmptyModelErr should return false for other errors")
		}
	})

	t.Run("IsInvalidSchemaErr", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", melange.ErrInvalidSchema)
		if !melange.IsInvalidSchemaErr(err) {
			t.Error("IsInvalidSchemaErr should return true for wrapped ErrInvalidSchema")
		}
		if melange.IsInvalidSchemaErr(errors.New("other error")) {
			t.Error("IsInvalidSchemaErr should return false for other errors")
		}
	})

	t.Run("IsMissingFunctionErr", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", melange.ErrMissingFunction)
		if !melange.IsMissingFunctionErr(err) {
			t.Error("IsMissingFunctionErr should return true for wrapped ErrMissingFunction")
		}
		if melange.IsMissingFunctionErr(errors.New("other error")) {
			t.Error("IsMissingFunctionErr should return false for other errors")
		}
	})
}

func TestSentinelErrors(t *testing.T) {
	// Verify sentinel errors have meaningful messages
	tests := []struct {
		err     error
		wantMsg string
	}{
		{melange.ErrNoTuplesTable, "melange_tuples view/table not found"},
		{melange.ErrMissingModel, "melange_model table missing"},
		{melange.ErrEmptyModel, "authorization model empty"},
		{melange.ErrInvalidSchema, "invalid schema"},
		{melange.ErrMissingFunction, "authorization function missing"},
	}

	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			if tt.err.Error() == "" {
				t.Error("error message should not be empty")
			}
		})
	}
}
