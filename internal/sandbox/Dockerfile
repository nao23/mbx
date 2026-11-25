# msbx's default image, embedded in msbx and built by it (see image.go)

# Only the uv binaries. Before the first FROM, as the ARG is used in FROM
ARG UV_VERSION=latest
FROM ghcr.io/astral-sh/uv:${UV_VERSION} AS uv

# Only the native binary from the npm package, so the image has no Node.js
FROM node:24-bookworm-slim AS claude-code
ARG CLAUDE_CODE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && cp /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe /claude

FROM ubuntu:26.04
# - docker.io / iptables / nftables: the sandbox's own Docker Engine
# - docker-compose-v2 / docker-buildx: docker compose, and buildx that compose's build uses
# - dnsmasq-base: relays container DNS (see internal/feature/docker.sh)
# - qemu-user / qemu-user-binfmt: run containers for other architectures, such as amd64
# - build-essential etc.: build Python packages with C extensions (mysqlclient, etc.)
# - socat: relays claude's MCP requests to the MCP gateway's socket over vsock
# - gh: GitHub CLI, authenticated with $GH_TOKEN by the github feature
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      git python3 python3-pip python3-venv curl ca-certificates jq ripgrep less vim-tiny unzip make \
      docker.io docker-compose-v2 docker-buildx iptables nftables dnsmasq-base qemu-user qemu-user-binfmt \
      build-essential pkg-config python3-dev default-libmysqlclient-dev socat gh \
 && rm -rf /var/lib/apt/lists/*
COPY --from=claude-code /claude /usr/local/bin/claude
COPY --from=uv /uv /uvx /usr/local/bin/
# Where uv tool install puts commands
ENV PATH="/root/.local/bin:${PATH}"
