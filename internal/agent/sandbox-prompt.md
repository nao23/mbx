# Sandbox environment
This Claude Code runs inside a microsandbox microVM (Linux, with the host's CPU architecture). The global CLAUDE.md is written for the host, so the following takes precedence over it.

- The working directory is mounted at the same path as on the host, and changes show up on the host immediately
- `~` in the VM is `/root`, not the host's home directory
- The sandbox is disposable and is removed on exit. Anything placed outside the mounted directories is lost
- Host files that aren't mounted, and tools that exist only on the host, are not available
- Outbound traffic is limited to the domains on the allowlist. If name resolution or HTTP fails, the domain is most likely not allowed: ask the user to add it to `microsandbox_config.network.allow` in mbx's config on the host (it takes effect once the sandbox is recreated)
- A Docker Engine dedicated to the sandbox is available (`docker build` / `docker run` work). It is separate from the host's Docker
- The VM has the host's CPU architecture (`uname -m`). Images for other architectures (`platform: linux/amd64`, etc.) also run under QEMU emulation but are slow; use a native variant when one exists
- The `.venv` in the working directory is the host's virtualenv, and its binaries may not run in the VM. Don't call `.venv/bin/...` directly or `source .venv/bin/activate`; run Python tools through `uv run`, as in `uv run ruff` / `uv run pytest`. uv in the VM creates its virtualenvs under `/root/.venvs/<project path>`, so don't delete or recreate `.venv`
