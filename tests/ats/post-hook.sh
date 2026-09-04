#!/usr/bin/env sh
# ATS scenario post-hook (app-tests-post-hook in .ats/main.yaml).
#
# ATS runs it after every scenario's tests with ATS_TEST_TYPE (smoke |
# functional | upgrade), ATS_HOOK_STAGE=post, ATS_RELEASE_NAMESPACE and
# KUBECONFIG in the environment (docs/TEST_CONTRACT.md in app-test-suite).
#
# All scenarios share the job's kind cluster. The smoke installs the candidate
# itself (app-tests-skip-app-deploy: true, so ATS uninstalls nothing) and the
# upgrade scenario then `helm install`s the latest stable chart under the same
# release name into the same namespace. Uninstall the smoke's release here so
# the stable install lands on a prepared namespace: the prerequisites the smoke
# applied (Gateway API CRDs, prerequisites/lab-dex.yaml) stay, the release
# goes. Nothing in examples/kind-lab-dex.yaml renders a `helm.sh/resource-policy:
# keep` resource (postgres and model serving are off), so `helm uninstall`
# leaves no adopted state behind.
set -eu

[ "${ATS_TEST_TYPE:-}" = "smoke" ] || exit 0

release="agent-platform-standalone"
namespace="${ATS_RELEASE_NAMESPACE:-agent-platform}"

echo "Uninstalling the smoke's release '${release}' from '${namespace}' so the upgrade scenario starts from the stable chart"
helm uninstall "${release}" --namespace "${namespace}" --wait --timeout 10m --ignore-not-found
