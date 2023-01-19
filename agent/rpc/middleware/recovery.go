package middleware

import (
	"fmt"

	"github.com/hashicorp/go-hclog"
)

// NewPanicHandler returns a RecoveryHandlerFunc type function
// to handle panic in RPC server's handlers.
func NewPanicHandler(logger hclog.InterceptLogger) RecoveryHandlerFunc {
	return func(p interface{}) (err error) {
		// Log the panic and the stack trace of the Goroutine that caused the panic.
		stacktrace := hclog.Stacktrace()
		logger.Error("panic serving grpc request",
			"panic", p,
			"stack", stacktrace,
		)

		// TODO: verify this is mapped to a proper error code/status in rpc?
		return fmt.Errorf("rpc: panic serving request")
	}
}

type RecoveryHandlerFunc func(p interface{}) (err error)
