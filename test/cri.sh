#!/usr/bin/env bash
# CRI gRPC smoke tests against a running boxr server.
# Usage: ./test/cri.sh [rootfs-path]
# Default rootfs: /home/ubuntu/boxr/rootfs
# Requires: go tool grpcurl, sudo boxr serve running on /tmp/boxr.sock

set -euo pipefail

SOCK="unix:///tmp/boxr.sock"
ROOTFS="${1:-/home/ubuntu/boxr/rootfs}"
GRPCURL="go tool grpcurl"

ok()   { echo "[PASS] $*"; }
fail() { echo "[FAIL] $*" >&2; exit 1; }
step() { echo; echo "=== $* ==="; }

grpc() {
  local method="$1"; shift
  $GRPCURL -plaintext "$@" "$SOCK" "$method"
}

grpc_d() {
  local method="$1"; shift
  local data="$1"; shift
  $GRPCURL -plaintext -d "$data" "$@" "$SOCK" "$method"
}

# ------------------------------------------------------------
step "Version"
out=$(grpc runtime.v1.RuntimeService/Version)
echo "$out"
echo "$out" | grep -q '"runtimeName": "boxr"' && ok "runtimeName=boxr" || fail "Version"

# ------------------------------------------------------------
step "RunPodSandbox"
out=$(grpc_d runtime.v1.RuntimeService/RunPodSandbox \
  '{"config":{"metadata":{"name":"test-pod","namespace":"default","uid":"abc123"}}}')
echo "$out"
SANDBOX_ID=$(echo "$out" | grep podSandboxId | sed 's/.*"\(.*\)".*/\1/')
[[ -n "$SANDBOX_ID" ]] && ok "sandbox=$SANDBOX_ID" || fail "RunPodSandbox: no podSandboxId"

# ------------------------------------------------------------
step "CreateContainer (echo hello)"
out=$(grpc_d runtime.v1.RuntimeService/CreateContainer "$(cat <<EOF
{
  "pod_sandbox_id": "$SANDBOX_ID",
  "config": {
    "metadata": {"name": "test-ctr"},
    "image":    {"image": "$ROOTFS"},
    "command":  ["/bin/sh"],
    "args":     ["-c", "echo hello && sleep 2"]
  }
}
EOF
)")
echo "$out"
CTR_ID=$(echo "$out" | grep containerId | sed 's/.*"\(.*\)".*/\1/')
[[ -n "$CTR_ID" ]] && ok "container=$CTR_ID" || fail "CreateContainer: no containerId"

# ------------------------------------------------------------
step "ContainerStatus (expect CREATED)"
out=$(grpc_d runtime.v1.RuntimeService/ContainerStatus \
  "{\"container_id\":\"$CTR_ID\"}")
echo "$out"
echo "$out" | grep -q "CONTAINER_CREATED" && ok "status=CREATED" || fail "expected CREATED"

# ------------------------------------------------------------
step "StartContainer"
out=$(grpc_d runtime.v1.RuntimeService/StartContainer \
  "{\"container_id\":\"$CTR_ID\"}")
echo "$out"
ok "StartContainer returned"

# ------------------------------------------------------------
step "ContainerStatus (expect RUNNING)"
out=$(grpc_d runtime.v1.RuntimeService/ContainerStatus \
  "{\"container_id\":\"$CTR_ID\"}")
echo "$out"
echo "$out" | grep -q "CONTAINER_RUNNING" && ok "status=RUNNING" || fail "expected RUNNING"

# ------------------------------------------------------------
step "Wait for container to exit (~3s)"
sleep 3

step "ContainerStatus (expect EXITED, exitCode=0)"
out=$(grpc_d runtime.v1.RuntimeService/ContainerStatus \
  "{\"container_id\":\"$CTR_ID\"}")
echo "$out"
echo "$out" | grep -q "CONTAINER_EXITED" && ok "status=EXITED" || fail "expected EXITED"

# exitCode 0 is the proto3 default so it may be omitted; grep for its absence of nonzero
if echo "$out" | grep -q '"exitCode"'; then
  code=$(echo "$out" | grep exitCode | grep -o '[0-9-]*')
  [[ "$code" == "0" ]] && ok "exitCode=0" || fail "exitCode=$code (expected 0)"
else
  ok "exitCode=0 (omitted, proto3 default)"
fi

# ------------------------------------------------------------
step "StartContainer again (expect FailedPrecondition)"
set +e
out=$(grpc_d runtime.v1.RuntimeService/StartContainer \
  "{\"container_id\":\"$CTR_ID\"}" 2>&1)
set -e
echo "$out"
echo "$out" | grep -qi "FailedPrecondition\|not in CREATED" \
  && ok "double-start rejected" || fail "expected FailedPrecondition"

# ------------------------------------------------------------
step "RunPodSandbox with missing config (expect InvalidArgument)"
set +e
out=$(grpc_d runtime.v1.RuntimeService/RunPodSandbox '{}' 2>&1)
set -e
echo "$out"
echo "$out" | grep -qi "InvalidArgument\|config is required" \
  && ok "missing config rejected" || fail "expected InvalidArgument"

echo
echo "All tests passed."
