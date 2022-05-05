#!/usr/bin/env bash

# This script verifies that all the e2e tests defined in test/e2e have
# a corresponding call in test/e2e/ci_test.go. The tests defined in
# ci_test.go file are invoked by the `make test-e2e` target (unless
# overridden with the TEST variable. The ci_test.go file provides
# ordering of tests that can be run in parallel and tests that must be
# executed in series. If a new e2e test is added then the `make
# verify` target will run this script and the script will list any
# Test functions by name that need to be added to test/e2e/ci_test.go.
# If any omissions are detected then `make verify` will fail.

set -e
set -u
set -o pipefail

thisdir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
e2e_test_file="$thisdir/../test/e2e/ci_test.go"

if [ ! -f "$e2e_test_file" ]; then
    echo "$e2e_test_file is missing." >&2
    exit 1
fi

testnames=$(cd ./test/e2e; go test -tags e2e -list . | grep '^Test' | grep -v '^TestAll$')

if [ -z "${testnames}" ]; then
    echo "Error: expected to find a long list of Test names; something is very wrong." >&2
    exit 1
fi

errors=0

for testname in $testnames; do
    matchstring="^[[:space:]]+t\.Run\(\"$testname\", $testname\)"
    if ! grep --extended-regexp --quiet "$matchstring" "$e2e_test_file"; then
        errors=1
        echo "Error: call to '$testname' not matched in $e2e_test_file" >&2
    fi
done

exit $errors
