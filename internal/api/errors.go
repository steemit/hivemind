package api

import "fmt"

// Error represents an API error
type Error struct {
	Code    int
	Message string
}

// NewError creates a new API error
func NewError(code int, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
	}
}

// Error implements the error interface
func (e *Error) Error() string {
	return fmt.Sprintf("API error %d: %s", e.Code, e.Message)
}

