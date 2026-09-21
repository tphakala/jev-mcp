package jev

import "errors"

// ErrValidation is returned by [Validate] for a request that the client
// rejects before any network call. It is wrapped with the offending question
// name (or a request-level detail) using %w, so callers match it with
// errors.Is and still see the specifics in the message.
var ErrValidation = errors.New("jev: invalid request")
