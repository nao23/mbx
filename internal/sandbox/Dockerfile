# mbx's default image, embedded in mbx and built by it (see image.go). It has no language toolchains: add
# them in an image built on this one

# Only the native binary from the npm package, so the image has no Node.js
FROM node:24-bookworm-slim AS claude-code
ARG CLAUDE_CODE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && cp /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe /claude

FROM ubuntu:26.04
# - docker.io / iptables / nftables: the sandbox's own Docker Engine
# - docker-compose-v2 / docker-buildx: docker compose, and buildx that compose's build uses
# - dnsmasq-base: relays container DNS (see internal/feature/docker.sh)
# - qemu-user / qemu-user-binfmt: run containers for architectures other than the VM's (amd64 on arm64, etc.)
# - build-essential: build packages with C extensions (pip, npm, etc.)
# - socat: relays claude's MCP requests to the MCP gateway's socket over vsock
# - gh: GitHub CLI, authenticated with $GH_TOKEN by the github feature
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      git curl ca-certificates jq ripgrep less vim-tiny unzip make \
      docker.io docker-compose-v2 docker-buildx iptables nftables dnsmasq-base qemu-user qemu-user-binfmt \
      build-essential socat gh \
 && rm -rf /var/lib/apt/lists/*
COPY --from=claude-code /claude /usr/local/bin/claude
