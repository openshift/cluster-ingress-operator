#!/bin/bash

go_files=$( find . -name '*.go' -not -path './vendor/*' -print )
bad_files=$(gofmt -s -l ${go_files})
if [[ -n "${bad_files}" ]]; then
    (>&2 echo "!!! gofmt needs to be run on the listed files")
	echo "${bad_files}"
    (>&2 echo "Try running 'gofmt -s -d [path]' or autocorrect with 'hack/verify-gofmt.sh | xargs -n 1 gofmt -s -w'")
    exit 1
fi
