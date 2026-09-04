#!/usr/bin/env sh
# ATS upgrade hook (upgrade-tests-upgrade-hook in .ats/main.yaml).
#
# `helm upgrade` never touches a chart's crds/ directories, so upgrading the
# umbrella needs the documented one-liner from the README first:
#
#   helm show crds <chart> | kubectl apply --server-side -f -
#
# ATS runs this hook twice around the `helm upgrade` to the candidate, with
# the context in environment variables (docs/TEST_CONTRACT.md in
# app-test-suite): ATS_HOOK_STAGE (pre_upgrade | post_upgrade),
# ATS_RELEASE_NAME, ATS_RELEASE_NAMESPACE, ATS_UPGRADE_FROM_VERSION,
# ATS_UPGRADE_TO_VERSION and KUBECONFIG. The pre_upgrade stage applies the
# CANDIDATE's CRDs (`helm show crds` prints the CRDs of every dependency too);
# post_upgrade is a no-op. helm and kubectl come with the ATS image.
set -eu

[ "${ATS_HOOK_STAGE}" = "pre_upgrade" ] || exit 0

# The CI job copies the candidate chart archive to the workdir root, which is
# also the hook's working directory; the release is named after the chart.
chart_archive="${ATS_RELEASE_NAME}-${ATS_UPGRADE_TO_VERSION}.tgz"
if [ ! -f "${chart_archive}" ]; then
  echo "candidate chart archive '${chart_archive}' not found in $(pwd)" >&2
  exit 1
fi

echo "Applying the candidate's CRDs before the upgrade (helm upgrade skips crds/)"
helm show crds "${chart_archive}" | kubectl apply --server-side -f -
