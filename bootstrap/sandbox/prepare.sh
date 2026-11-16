#!/usr/bin/env bash

set -euo pipefail
set +x
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
readonly REPO_DIR
readonly WORK_DIR="${REPO_DIR}/.work/bootstrap-sandbox"
readonly NAMESPACE="pt-bootstrap-sandbox"
readonly ENCRYPTION_FILE="${WORK_DIR}/encryption.hcl"
readonly FOUNDATION_ENCRYPTION_FILE="${WORK_DIR}/foundation-encryption.hcl"

fail() {
  echo "sandbox bootstrap: $*" >&2
  exit 1
}

kubectl() {
  command kubectl --context=docker-desktop "$@"
}

for tool in base64 cmp gcloud kubectl openssl tr; do
  command -v "${tool}" >/dev/null || fail "required command '${tool}' is missing"
done

[ "$(command kubectl config current-context)" = "docker-desktop" ] ||
  fail "select docker-desktop explicitly; this script never switches contexts"

kubectl get deployment tofu-controller --namespace=flux-system >/dev/null ||
  fail "the local Flux floor must already be running"
kubectl wait deployment/tofu-controller --namespace=flux-system \
  --for=condition=Available --timeout=30s >/dev/null

gcloud resource-manager folders describe 648523111758 --format='value(name)' >/dev/null ||
  fail "the gcloud user cannot read the sandbox folder"
gcloud auth application-default print-access-token >/dev/null ||
  fail "run gcloud auth application-default login"

mkdir -p "${WORK_DIR}"
chmod 700 "${WORK_DIR}"

# This namespace is separate from the disposable integration fixtures.
existing_namespace="$(kubectl get namespace "${NAMESPACE}" --ignore-not-found \
  --output=jsonpath='{.metadata.name}')"
if [ -n "${existing_namespace}" ]; then
  owner="$(kubectl get namespace "${NAMESPACE}" \
    --output=jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')"
  [ "${owner}" = "pt-techne-floor" ] ||
    fail "the bootstrap namespace exists but is not owned by pt-techne-floor"
fi
kubectl apply --filename="${REPO_DIR}/floors/bootstrap-sandbox/namespace.yaml" >/dev/null

if [ ! -s "${ENCRYPTION_FILE}" ]; then
  existing_secret="$(kubectl get secrets --namespace="${NAMESPACE}" \
    --output=jsonpath='{.items[*].metadata.name}')"
  [ -z "${existing_secret}" ] ||
    fail "bootstrap secrets already exist; restore the original .work/bootstrap-sandbox/encryption.hcl rather than generate a new key"

  passphrase="$(openssl rand -base64 32)"
  cat >"${ENCRYPTION_FILE}" <<HCL
key_provider "pbkdf2" "default" {
  passphrase = "${passphrase}"
}
method "aes_gcm" "default" {
  keys = key_provider.pbkdf2.default
}
state {
  method   = method.aes_gcm.default
  enforced = true
}
plan {
  method   = method.aes_gcm.default
  enforced = true
}
HCL
  unset passphrase
fi

if [ -n "$(kubectl get secret floor-bootstrap-state-env --namespace="${NAMESPACE}" \
  --ignore-not-found --output=jsonpath='{.metadata.name}')" ]; then
  kubectl get secret floor-bootstrap-state-env --namespace="${NAMESPACE}" \
    --output=jsonpath='{.data.TF_ENCRYPTION}' |
    base64 --decode |
    cmp --silent - "${ENCRYPTION_FILE}" ||
    fail "the local encryption key does not match the bootstrap Secret; refusing to replace it"
fi

kubectl create secret generic floor-bootstrap-state-env \
  --namespace="${NAMESPACE}" --from-file="TF_ENCRYPTION=${ENCRYPTION_FILE}" \
  --dry-run=client --output=yaml |
  kubectl apply --server-side --field-manager=sandbox-bootstrap --filename=- >/dev/null

# Only a short-lived access token enters Kubernetes, never the ADC refresh token.
gcloud auth application-default print-access-token |
  tr --delete '\n' |
  kubectl create secret generic floor-bootstrap-auth --namespace="${NAMESPACE}" \
    --from-file=GOOGLE_OAUTH_ACCESS_TOKEN=/dev/stdin --dry-run=client --output=yaml |
  kubectl apply --server-side --field-manager=sandbox-bootstrap --filename=- >/dev/null

seed_ready="$(kubectl get terraform bootstrap-sandbox --namespace="${NAMESPACE}" \
  --ignore-not-found --output=jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
if [ "${seed_ready}" = "True" ]; then
  kms_key="$(gcloud kms keys describe default --keyring=state-encryption --location=us \
    --project=pt-bootstrap-648523111758-sb --format='value(name)')"
  [ -n "${kms_key}" ] || fail "the applied seed KMS key could not be resolved"

  cat >"${FOUNDATION_ENCRYPTION_FILE}" <<HCL
key_provider "gcp_kms" "default" {
  key_length         = 32
  kms_encryption_key = "${kms_key}"
}
method "aes_gcm" "default" {
  keys = key_provider.gcp_kms.default
}
state {
  method   = method.aes_gcm.default
  enforced = true
}
plan {
  method   = method.aes_gcm.default
  enforced = true
}
remote_state_data_sources {
  default {
    method = method.aes_gcm.default
  }
}
HCL

  kubectl create secret generic floor-foundation-state-env \
    --namespace="${NAMESPACE}" --from-file="TF_ENCRYPTION=${FOUNDATION_ENCRYPTION_FILE}" \
    --dry-run=client --output=yaml |
    kubectl apply --server-side --field-manager=sandbox-bootstrap --filename=- >/dev/null
fi

kubectl apply --filename=- >/dev/null <<YAML
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: bootstrap-floor-config
  namespace: flux-system
spec:
  interval: 1m
  ref:
    branch: bootstrap-sandbox-from-local-floor
  url: https://github.com/osinfra-io/pt-techne-floor
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: bootstrap-sandbox
  namespace: flux-system
spec:
  deletionPolicy: Orphan
  interval: 1m
  path: ./floors/bootstrap-sandbox
  prune: false
  sourceRef:
    kind: GitRepository
    name: bootstrap-floor-config
  wait: false
YAML

echo "Sandbox bootstrap registered; no cloud apply was approved."
echo "From pt-pneuma, run FLOOR_ADDITIONAL_NAMESPACES=${NAMESPACE} tests/integration/floor/setup.sh to expose it through floor-api."
echo "Review ${NAMESPACE}/bootstrap-sandbox through the floor's get_workspace and pending_approvals tools; a human must approve."
echo "Rerun this script before approving if the Google access token has expired."
echo "After the seed is Ready, rerun this script to prepare the Logos foundation's GCS/KMS state."
echo "Keep ${ENCRYPTION_FILE} and the ${NAMESPACE} state Secrets safe; do not reset Docker Desktop."
