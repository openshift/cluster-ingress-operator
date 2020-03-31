#!/bin/bash
set -euo pipefail

function print_failure {
  echo "There appear to be uncommitted changes to go.mod. Please run 'go tidy' and 'go vendor'."
  exit 1
}

go mod vendor
go mod tidy

test -z "$(git status --porcelain)" || print_failure
