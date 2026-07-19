package session

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("review session not found")

type SessionStore interface {
	Create(context.Context, ReviewSession) error
	Get(context.Context, string) (ReviewSession, error)
	Update(context.Context, ReviewSession) error
	Delete(context.Context, string) error
	List(context.Context) ([]ReviewSession, error)
}
