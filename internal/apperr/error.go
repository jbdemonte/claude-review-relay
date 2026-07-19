package apperr

import (
	"errors"
	"fmt"
)

type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
	Cause   error          `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code, message string, details map[string]any) *Error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{Code: code, Message: message, Details: details}
}

func Wrap(code, message string, cause error, details map[string]any) *Error {
	e := New(code, message, details)
	e.Cause = cause
	return e
}

func From(err error) *Error {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return Wrap("internal_error", "An internal error occurred.", err, nil)
}
