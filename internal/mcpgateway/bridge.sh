# Relays 127.0.0.1:$port in the VM to the MCP gateway's socket for this sandbox, which msb exposes at vsock
# port $port. BridgeScript sets $port before this.

# /run outlives a stop and pids are reused after the restart, so the pid's process must be socat
pid=/run/mbx-mcp-bridge.pid
[ -f "$pid" ] && [ "$(cat "/proc/$(cat "$pid")/comm" 2>/dev/null)" = socat ] && exit 0

if ! command -v socat >/dev/null; then
  echo "warning: socat is missing; MCP servers are unavailable" >&2
  exit 0
fi

nohup socat "TCP-LISTEN:$port,bind=127.0.0.1,reuseaddr,fork" "VSOCK-CONNECT:2:$port" \
  >/var/log/mbx-mcp-bridge.log 2>&1 &
echo $! >"$pid"
