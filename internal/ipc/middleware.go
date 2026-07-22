package ipc

import (
	"fmt"
	"log"
	"runtime/debug"
)

// recoverPanic wraps a handler function with panic recovery.
// If a panic occurs, it logs the panic value and stack trace, then returns a
// structured error. On normal execution the handler result is passed through
// unchanged.
func recoverPanic(handlerName string, handler func() (interface{}, error)) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			logPanicRecovery(handlerName, r)
			result = nil
			err = fmt.Errorf("panic recovered in %s: %v", handlerName, r)
		}
	}()
	return handler()
}

// logPanicRecovery logs a panic with full stack trace and operation context.
// Panics are logged at WARNING level because they are recoverable; the IPC
// server continues serving requests after recovery.
func logPanicRecovery(context string, panicValue interface{}) {
	log.Printf("WARNING: PANIC in %s: %v", context, panicValue)
	log.Printf("Stack trace:\n%s", debug.Stack())
}
