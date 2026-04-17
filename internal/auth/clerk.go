// Package auth provides Clerk JWT verification for the ingest service.
// This is a scaffolding stub — handler wiring is a separate architectural task.
package auth

import (
	"context"
	"errors"
)

// ErrNotImplemented indicates the Clerk verifier has not been wired yet.
var ErrNotImplemented = errors.New("clerk JWT verification not yet implemented")

// VerifyJWT validates a Clerk-issued JWT and returns the session claims.
// TODO: wire clerk.SetKey with CLERK_SECRET_KEY at service startup, then use
// jwt.Verify from clerk-sdk-go/v2/jwt. Tracking: enable once /api routes
// require auth.
func VerifyJWT(ctx context.Context, token string) (sessionID string, err error) {
	_ = ctx
	_ = token
	return "", ErrNotImplemented
}
