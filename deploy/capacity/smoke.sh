#!/usr/bin/env bash
# Local, synthetic input only. Build all three binaries before invoking.
set -euo pipefail
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
app_bin="${CAPACITY_APP:-$repo_dir/bin/cmt-top}"
replay_bin="${CAPACITY_REPLAY:-$repo_dir/bin/cmt-top-replay}"
load_bin="${CAPACITY_LOAD:-$repo_dir/bin/cmt-top-load}"
for binary in "$app_bin" "$replay_bin" "$load_bin"; do
  if [[ ! -x "$binary" ]]; then
    echo "Missing binary: $binary. Build cmt-top, cmt-top-replay, and cmt-top-load first." >&2
    exit 1
  fi
done
output_dir="${CAPACITY_OUTPUT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/cmt-top-capacity.XXXXXX")}"
mkdir -p "$output_dir"
# Bind ephemeral sockets together so selected ports are distinct. Processes
# subsequently fail closed if an unrelated process wins the short bind race.
read -r rpc_port web_port metrics_port < <(python3 - <<'PY'
import socket
sockets = [socket.socket() for _ in range(3)]
for s in sockets:
    s.bind(('127.0.0.1', 0))
print(*(s.getsockname()[1] for s in sockets))
PY
)
fixture_url="http://127.0.0.1:$rpc_port"
target_url="http://127.0.0.1:$web_port"
replay_pid=""
app_pid=""
cleanup() {
  [[ -z "$app_pid" ]] || kill "$app_pid" 2>/dev/null || true
  [[ -z "$replay_pid" ]] || kill "$replay_pid" 2>/dev/null || true
  [[ -z "$app_pid" ]] || wait "$app_pid" 2>/dev/null || true
  [[ -z "$replay_pid" ]] || wait "$replay_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM
cat > "$output_dir/config.toml" <<EOF
[chain]
name = "Synthetic capacity replay"
lcd = "$fixture_url"
monitored_rpcs = []
explorer_url = ""
[[chain.rpc]]
url = "$fixture_url"
primary = true
[ui]
mode = "web"
[ui.web]
listen = "127.0.0.1:$web_port"
token = ""
[obs]
metrics_listen = "127.0.0.1:$metrics_port"
log_level = "warn"
EOF
replay_args=(-listen "127.0.0.1:$rpc_port" -profile "${CAPACITY_PROFILE:-mainnet45}" -seed "${CAPACITY_SEED:-1}" -rounds "${CAPACITY_ROUNDS:-1}" -event-interval "${CAPACITY_EVENT_INTERVAL:-1ms}" -block-interval "${CAPACITY_BLOCK_INTERVAL:-1s}")
if [[ "${CAPACITY_STALLED:-0}" == "1" ]]; then replay_args+=(-stalled); fi
env -i PATH="$PATH" "$replay_bin" "${replay_args[@]}" > "$output_dir/replay.log" 2>&1 &
replay_pid=$!
env -i PATH="$PATH" "$app_bin" --config "$output_dir/config.toml" > "$output_dir/app.log" 2>&1 &
app_pid=$!
echo "Capacity artifacts: $output_dir" >&2
# The load command verifies fixture identity, configured app RPC, chain identity
# and readiness before offering sessions. It rejects redirects/public targets.
"$load_bin" -target "$target_url" -fixture "$fixture_url" -metrics "http://127.0.0.1:$metrics_port" \
  -users "${CAPACITY_USERS:-150}" -duration "${CAPACITY_DURATION:-10s}" -ramp "${CAPACITY_RAMP:-3s}" \
  -investigate-percent "${CAPACITY_INVESTIGATE_PERCENT:-50}" -strict | tee "$output_dir/result.json"
