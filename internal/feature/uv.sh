# The shared .venv holds the host OS's binaries, so the wrapped uv puts each project's virtualenv in the VM.
# The project is the nearest directory up with uv.lock, else pyproject.toml
real=$(command -v uv) || exit 0
[ -e "$real-real" ] && exit 0
mv "$real" "$real-real"
cat >"$real" <<'EOF'
#!/bin/sh
# msbx: keep the project's virtualenv in the VM, not in .venv shared with the host
if [ -z "$UV_PROJECT_ENVIRONMENT" ]; then
  for f in uv.lock pyproject.toml; do
    d=$PWD
    until [ "$d" = / ] || [ -f "$d/$f" ]; do d=$(dirname "$d"); done
    [ "$d" != / ] && break
  done
  [ "$d" = / ] || export UV_PROJECT_ENVIRONMENT="/root/.venvs$d"
fi
exec "$0-real" "$@"
EOF
chmod +x "$real"
