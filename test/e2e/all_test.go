//go:build e2e
// +build e2e

package e2e

import "testing"

// TestAll is the entrypoint for `make test-e2e` unless you override
// with: make TEST=Test<foo> test-e2e.
//
// The overriding goal of this test is to run as many tests in
// parallel as possible before running those tests that must run serially. There
// are two goals: 1) cut down on test execution time and 2) provide
// explicit ordering for tests that do not expect a rolling update of
// ingresscontroller pods because a previous test modified the
// ingressconfig object and the defer logic for cleanup is still
// runnng when the new test starts.
func TestAll(t *testing.T) {
	// This call to Run() will not return until all of its
	// parallel subtests complete. Each "parallel" test must
	// invoke t.Parallel().
	t.Run("parallel", func(t *testing.T) {
		t.Run("Test_IdleConnectionTerminationPolicy", Test_IdleConnectionTerminationPolicy)
	})

	t.Run("serial", func(t *testing.T) {
	})
}
