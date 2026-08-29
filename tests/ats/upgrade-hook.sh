#!/usr/bin/env sh
# ATS upgrade hook (upgrade-tests-upgrade-hook in .ats/main.yaml).
#
# `helm upgrade` never touches a chart's crds/ directories, so upgrading the
# umbrella needs the documented one-liner from the README first:
#
#   helm show crds <chart> | kubectl apply --server-side -f -
#
# ATS calls this hook twice around the App CR upgrade with positional args:
#   <stage> <app_name> <from_version> <to_version> <kube_config_path> <deploy_namespace>
# The pre_upgrade stage applies the CANDIDATE's CRDs (`helm show crds` prints
# the CRDs of every dependency too); post_upgrade is a no-op. The ATS
# container ships kubectl but no helm, so a pinned helm is fetched first —
# the same release line .circleci/custom.yml pins for `make verify`.
set -eu

stage="$1"
app_name="$2"
to_version="$4"
kube_config_path="$5"

[ "${stage}" = "pre_upgrade" ] || exit 0

if ! command -v helm >/dev/null 2>&1; then
  curl -fsSL https://get.helm.sh/helm-v3.21.4-linux-amd64.tar.gz | tar xz -C /tmp
  PATH="/tmp/linux-amd64:${PATH}"
fi

# The orb copies the candidate chart archive to the workdir root, which is
# also the hook's working directory.
chart_archive="${app_name}-${to_version}.tgz"
if [ ! -f "${chart_archive}" ]; then
  echo "candidate chart archive '${chart_archive}' not found in $(pwd)" >&2
  exit 1
fi

echo "Applying the candidate's CRDs before the upgrade (helm upgrade skips crds/)"
helm show crds "${chart_archive}" | kubectl --kubeconfig "${kube_config_path}" apply --server-side -f -
