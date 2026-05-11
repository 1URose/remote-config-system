package domain

import (
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("config item not found")

type VersionConflictError struct {
	Key             string
	ExpectedVersion int64
	CurrentVersion  int64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict for key %q: expected=%d current=%d", e.Key, e.ExpectedVersion, e.CurrentVersion)
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

func NewValidationError(message string) error {
	return &ValidationError{Message: message}
}
