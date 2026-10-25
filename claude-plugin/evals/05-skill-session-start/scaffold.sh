#!/usr/bin/env bash
# Every case seeds the same Acme sandbox; the shared script owns the details.
# (case.yaml's scaffold_script must live inside the case directory.)
exec bash "$(dirname "$0")/../scaffold/seed.sh"
