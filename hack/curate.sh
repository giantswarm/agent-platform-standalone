#!/usr/bin/env bash
# Generate (or, with --check, verify) the agent-platform-standalone chart from
# the fleet meta-package. See hack/curate/main.go and curate.yaml.
set -euo pipefail

cd "$(dirname "$0")/.."
exec go run ./hack/curate "$@"
