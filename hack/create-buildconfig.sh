#!/bin/bash

set -euo pipefail

if [[ -z "${GIT_BRANCH+1}" ]]
then
    GIT_BRANCH="$(git symbolic-ref --short HEAD)"
fi

if [[ -z "${GIT_URL+1}" ]]
then
    if ! GIT_REMOTE="$(git config "branch.${GIT_BRANCH}.pushDefault")" ||
            [[ "$GIT_REMOTE" = '.' ]]
    then GIT_REMOTE="$(git config 'remote.pushDefault')"
    fi
    GIT_URL=$(git config "remote.${GIT_REMOTE}.url")
fi

if [[ "$GIT_URL" =~ ^git@ ]]
then
    # Convert git@host:user/repo to https://host/user/repo.
    GIT_URL="${GIT_URL/://}"
    GIT_URL="https://${GIT_URL#git@}"
fi

oc process -f "$(dirname "$0")/buildconfig.yaml" \
   -p "GIT_URL=$GIT_URL" \
   -p "GIT_BRANCH=$GIT_BRANCH" \
    | oc -n openshift-ingress-operator apply -f -
