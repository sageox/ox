#!/bin/sh
# Copies the starter project into the run folder as a git repo with one commit,
# the state a user is in before they set anything up.
set -eu
cp -R "$FIREGOAT_OUT/starter-app/." .
git init -q -b main
git add -A
git -c user.name='Avery' -c user.email='avery@example.com' commit -qm 'initial commit'
