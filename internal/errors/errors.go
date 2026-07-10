// Package errors defines standard error sentinels for the LQBOT framework.
package errors

import "errors"

var (
	ErrTokenExpired      = errors.New("access token expired")
	ErrRateLimited       = errors.New("rate limit exceeded")
	ErrShutdownTimeout   = errors.New("shutdown timeout")
	ErrInvalidConfig     = errors.New("invalid configuration")
	ErrAdapterNotStarted = errors.New("adapter not started")
)
