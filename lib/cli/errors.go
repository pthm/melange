// Package cli provides shared configuration and utilities for the melange CLI.
package cli

import (
	"errors"
	"fmt"
	"os"
)

// Exit codes per spec.
const (
	ExitSuccess     = 0
	ExitGeneral     = 1
	ExitConfig      = 2
	ExitSchemaParse = 3
	ExitDBConnect   = 4
)

// ExitError wraps an error with an exit code.
type ExitError struct {
	Code    int
	Message string
	Err     error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *ExitError) Unwrap() error {
	return e.Err
}

// ExitWithError prints the error and exits with the appropriate code.
func ExitWithError(err error) {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		fmt.Fprintln(os.Stderr, "Error:", exitErr.Error())
		os.Exit(exitErr.Code)
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(ExitGeneral)
}

// ConfigError creates an ExitError with ExitConfig code.
func ConfigError(msg string, err error) *ExitError {
	return &ExitError{Code: ExitConfig, Message: msg, Err: err}
}

// SchemaParseError creates an ExitError with ExitSchemaParse code.
func SchemaParseError(msg string, err error) *ExitError {
	return &ExitError{Code: ExitSchemaParse, Message: msg, Err: err}
}

// DBConnectError creates an ExitError with ExitDBConnect code.
func DBConnectError(msg string, err error) *ExitError {
	return &ExitError{Code: ExitDBConnect, Message: msg, Err: err}
}

// GeneralError creates an ExitError with ExitGeneral code.
func GeneralError(msg string, err error) *ExitError {
	return &ExitError{Code: ExitGeneral, Message: msg, Err: err}
}
