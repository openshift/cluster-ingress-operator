// Package extlogs provides helper functions for printing warnings
// during Ginkgo test runs.
//
// Why we use GinkgoWriter:
// -------------------------
// In Ginkgo, using fmt.Println() or log.Print() may not show messages.
// That's why we use fmt.Fprintf(ginkgo.GinkgoWriter, ...) instead of fmt.Println().
// This file is based on https://raw.githubusercontent.com/openshift/operator-framework-operator-controller/refs/heads/main/openshift/tests-extension/pkg/extlogs/warnings.go
package framework

import (
	"fmt"
	"runtime/debug"

	ginkgo "github.com/onsi/ginkgo/v2"
)

// Warn prints a simple warning message to the Ginkgo output.
// Example: Warn("something might be wrong")
func Warn(msg string) {
	fmt.Fprintf(ginkgo.GinkgoWriter, "[WARN] %s", msg)
}

// WarnError prints an error as a warning, if the error is not nil.
// Example: WarnError(err) will print "[WARN] error: ..." if err != nil
func WarnError(err error) {
	if err != nil {
		fmt.Fprintf(ginkgo.GinkgoWriter, "[WARN] error: %v", err)
	}
}

// WarnContext prints a warning with context + error, if the error is not nil.
// Example: WarnContext("failed to load config", err)
func WarnContext(context string, err error) {
	if err != nil {
		fmt.Fprintf(ginkgo.GinkgoWriter, "[WARN] %s: %v", context, err)
	}
}

// WarnContextf prints a formatted warning message with context.
// This is like printf + context-aware logging.
// Example: WarnContextf("unexpected value: %d", value)
func WarnContextf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(ginkgo.GinkgoWriter, "[WARN] %s", msg)
}

// Info prints an informational message to the Ginkgo output.
// Example: Info("starting test suite")
func Info(msg string) {
	fmt.Fprintf(ginkgo.GinkgoWriter, "[INFO] %s", msg)
}

// Infof prints a formatted informational message (like fmt.Printf).
// Example: Infof("using config: %s", path)
func Infof(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(ginkgo.GinkgoWriter, "[INFO] %s", msg)
}

// FatalErr exits the test in case a fatal error has occurred.
func FatalErr(msg interface{}) {
	// the path that leads to this being called isn't always clear...
	fmt.Fprintln(ginkgo.GinkgoWriter, string(debug.Stack()))
	Failf("%v", msg)
}

// Failf logs the fail info, including a stack trace starts with its direct caller
// (for example, for call chain f -> g -> Failf("foo", ...) error would be logged for "g").
func Failf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	skip := 1
	ginkgo.Fail(msg, skip)
	panic("unreachable")
}

// Fail is an alias for ginkgo.Fail.
var Fail = ginkgo.Fail
