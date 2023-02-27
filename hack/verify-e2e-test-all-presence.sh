#!/usr/bin/env bash

# This script verifies that all the e2e tests defined in package
# test/e2e have a corresponding invocation in the TestAll function
# defined in test/e2e/all_test.go.
#
# The TestAll function provides ordering of all e2e tests. Tests that
# can be run in parallel are invoked first and will run to completion
# before starting those that must run serially.
#
# The CI job runs `make verify` before starting the e2e tests and the
# verify target will run this script. If this script detects any Go
# Test function by name that is not invoked by the TestAll function
# then it will list the omission and exit with an error, preventing
# the e2e tests from starting.

# This script has been tested on Linux and macOS.

set -u
set -o pipefail

thisdir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
e2e_test_dir="$thisdir/../test/e2e"
e2e_test_file="$e2e_test_dir/all_test.go"

if ! [[ -f ${e2e_test_file} ]]; then
    echo "error: $e2e_test_file is missing." >&2
    exit 1
fi

# "go test -list" must run in the directory where the test files are.
pushd "$e2e_test_dir" >/dev/null || {
    echo "error: pushd $e2e_test_dir failed" >&2;
    exit 2                      # ENOENT
}

go_test_list_output=$(E2E_TEST_MAIN_SKIP_SETUP=1 go test -list . -tags e2e)
go_test_list_status=$?

if [[ $go_test_list_status -ne 0 ]]; then
    echo "error: go test -list failed" >&2
    echo "$go_test_list_output" >&2
    exit $go_test_list_status
fi

popd >/dev/null || {
    echo "error: popd failed" >&2;
    exit 1
}

errors=0

for i in $go_test_list_output; do
    if ! [[ $i =~ ^Test ]] || [[ $i == "TestAll" ]]; then
        continue
    fi
    pattern="^	\+t\.Run(\"$i\", $i)$" # ^multiple<TAB>s.
    if ! grep -q -e "$pattern" -- "$e2e_test_file"; then
        echo "error: test function '$i' not called by 'TestAll' in $e2e_test_file" >&2
        errors=1
    fi
done

exit $errors
