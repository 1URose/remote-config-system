package domain

import (
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("config item not found")

type NamespaceLockedError struct {
	Namespace string
}

func (e *NamespaceLockedError) Error() string {
	return fmt.Sprintf("namespace %q is locked by another write operation", e.Namespace)
}

type ResourceLockedError struct {
	Resource  string
	Namespace string
	Key       string
}

func (e *ResourceLockedError) Error() string {
	return fmt.Sprintf("%s %q/%q is locked by another write operation", e.Resource, e.Namespace, e.Key)
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
