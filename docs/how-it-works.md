# How it works

What mbx sets up in the sandbox, beyond the steps in the README's How it works.

## Network

How mbx sets up the VM's network on top of microsandbox's. The allowlist is `microsandbox_config.network.allow` in the config.

- Traffic to domains not on the allowlist is blocked
- TLS is intercepted (decrypted) only for secret destinations (`api.anthropic.com`, the ECR registry, `github.com`, etc.). Other allowed domains aren't intercepted (`tls.bypass`), as not every client trusts microsandbox's CA (containers get it only through the CA variables mbx sets, and Java and the like use their own trust store)
- microsandbox replaces lists whole when layering configs, so for `network` mbx adds its entries first and passes it as one config
- A rule with only a domain (`github.com`, etc.) allows all ports. To restrict ports, write e.g. `github.com:tcp:443`
- Rules work by domain, IP and port only. microsandbox can't restrict traffic by HTTP method or path (e.g. allow GET but not POST to an API)
- For connections matching a domain rule, microsandbox waits up to 5 seconds for the first bytes sent (to read the SNI or Host and check the connection is to an allowed domain). Protocols where the server speaks first, like SSH, wait 5 seconds on every connection. Connections allowed by an IP rule don't wait

### Hosts mbx adds

A secret's destination is added to the allowlist and to the TLS interception targets. The agent and the features add the hosts they need:

| Setting | Hosts |
|---|---|
| `claude` | `api.anthropic.com`, `platform.claude.com` |
| `docker.ecr` | The registry, and the S3 bucket holding its image layers (`prod-<region>-starport-layer-bucket.s3.<region>.amazonaws.com`) |
| `github` | `github.com`, `api.github.com`, `codeload.github.com`, `*.githubusercontent.com`, `ghcr.io` |

### GitHub

- `github.fast_ssh_keyscan` fetches GitHub's git IP ranges from its public `api.github.com/meta` and allows port 22 to them as IP rules placed before the domains. SSH to GitHub (`ssh-keyscan` during builds, etc.) then connects without waiting
- `github.allow` adds groups of GitHub hosts. `actions_storage` allows the Actions artifact and log storage hosts (`productionresultssa*.blob.core.windows.net`, for `gh run download` and `gh run view --log`) listed in the same `api.github.com/meta`. They're Azure Blob Storage accounts, so mbx allows only GitHub's, not `*.blob.core.windows.net`
- git in the VM rewrites `git@github.com:` / `ssh://git@github.com/` to HTTPS, so it uses the token instead of SSH

## Docker in the VM

- `/var/lib/docker` is an ext4 disk dedicated to the sandbox, since containers can't be created on the root filesystem (overlay)
- Container DNS goes through dnsmasq in the VM (`172.17.0.1`). mbx starts dockerd with `--dns 172.17.0.1` to set this up (if the image's `daemon.json` has `dns`, that's used instead). Some routers answer a non-EDNS query with more than 512 bytes; microsandbox's DNS can't receive it all and returns SERVFAIL, and musl (Alpine-based) containers fail to resolve names. dnsmasq keeps answers small to prevent this
- Containers and `docker build`'s `RUN` steps trust microsandbox's CA (for intercepted hosts like `github.com`): dockerd runs runc through a wrapper that mounts the VM's CA bundle at `/dev/mbx-ca-bundle.crt` and points `SSL_CERT_FILE`, `GIT_SSL_CAINFO`, `CURL_CA_BUNDLE` and `REQUESTS_CA_BUNDLE` at it, and `NODE_EXTRA_CA_CERTS` at the CA alone (`/dev/mbx-ca.crt`), unless the container sets them. Nothing is left in built images. Tools with their own trust store (Java, etc.) don't use it
- Binaries for other architectures run under QEMU (user mode). Speed is about a third to a quarter of native

## Sharing with the host's `~/.claude`

Claude Code's config dir (`CLAUDE_CONFIG_DIR`) lives in the VM, with only parts of the host's `~/.claude` overlaid.

| Host | In the VM | Access |
|---|---|---|
| `projects/`, `file-history/`, `history.jsonl` | Same layout | Read-write |
| `CLAUDE.md`, `settings.json` | Same layout | Read-only |
| `remote-settings-consent.json` | Same layout | Read-only |
| `skills/<name>` in `host_share.skills` | Same layout | Read-only |

Skills are shared only when listed in `host_share.skills` (`["*"]` for all), as some rely on host tools that aren't in the VM. Each is mounted from where its symlink points, as a symlink out of `~/.claude` would dangle in the VM. Only directories with `SKILL.md` count as skills; others in `skills/` (such as org-synced ones) aren't shared.

Everything else, such as `.claude.json` and the plugin cache, is created in the VM and removed with the sandbox. Files that `settings.json` refers to (a status line script, hooks, etc.) aren't mounted; add them with `microsandbox_flags.volume`, at the path `settings.json` names.

`projects/` is mounted whole, so claude in the VM creates the working directory's session directory there itself, only when it starts a session. This also means the VM can read and write the session history of every project, not just the working directory's.

## MCP gateway

claude in the VM uses remote MCP servers and claude.ai connectors through an MCP gateway on the host (`mbx mcp-gw`), which forwards the requests with the tokens, so they stay out of the VM. `mbx run` starts it when needed.

### How the VM reaches it

The gateway opens a unix socket for each sandbox, which msb exposes to that sandbox only, at a vsock port. In the VM, socat relays `127.0.0.1:<mcp_gateway.port>` to it, and claude's MCP config points there. So the VM holds no credentials, other sandboxes can't use its servers, and the socket tells the gateway which sandbox a request comes from. A custom image needs `socat` for MCP servers.

### Server definitions

```yaml
mcp_gateway:
  port: 41837
  servers:
    sentry:
      url: https://mcp.example.com/mcp
      client_id: abc123            # for a server without dynamic client registration
      callback_port: 13579         # the redirect port registered for client_id
      client_secret_source: {}     # for a confidential client_id (a secret source, as in secret_sources)
      scope: ""                    # default: what the server asks for
```

A server in `claude.mcp_servers` is defined by its `mcp_gateway.servers` entry, with what the entry leaves out taken from the host claude's user-scope server of the same name (`mcpServers` in `~/.claude.json`, as added with `claude mcp add --scope user`): the `url`, and when the url is the same, `client_id` and `callback_port` (`oauth.clientId` and `oauth.callbackPort`). So a server the host's claude already has needs no entry. Only the definition is read from there; the host claude's tokens aren't used.

### Tokens

- **`mcp_servers`: mbx's own login.** Log in once with `mbx mcp login [<server>]` (without one, to the servers in `claude.mcp_servers` not logged in to; `mbx mcp logout` likewise logs out of them all). Without a login, `mbx run` warns and the server fails to connect in the VM; logging in then and reconnecting (`/mcp` in claude) makes it work, as the gateway reads the login on each connection until it has one. mbx finds the server's OAuth authorization server as MCP clients do (its protected resource metadata, then the authorization server's metadata), registers itself as a client by dynamic client registration unless `client_id` is set, and opens the browser to authorize, with PKCE. The redirect goes to `http://localhost:<callback_port>/callback` (a random port by default; servers whose `client_id` was registered with a fixed redirect need that port). The login is saved in the keyring under service `mbx-mcp-gateway`: account `<server>` holds the login (url, client, expiry), and `<server>:access` and `<server>:refresh` the tokens. The gateway refreshes them when they expire. Changing `url` needs a new login
- **`claude_ai_connectors`: the host claude's claude.ai token.** claude.ai connectors can only be used with the claude.ai login, so the gateway reads the host claude's token (from the keychain on macOS, `~/.claude/.credentials.json` on Linux) only for these. The gateway doesn't refresh it, so when it expires, use the host's claude once (`mbx mcp` shows expiry)

### claude.ai connectors

Connectors get the same names in the VM as on the host (`claude_ai_Slack`), so tool names (`mcp__claude_ai_Slack__...`) and permission settings in `settings.json` match the host's. Tools blocked on claude.ai are removed from the list by the gateway, and calls to them are refused.

### Restarting

Run `mbx mcp-gw restart` after changing a connector's tool permissions on claude.ai, logging in to a server at a new url, or updating mbx.

### stdio servers

Only remote (HTTP) servers go through the gateway. A stdio server in a repository's `.mcp.json` is started by claude in the VM itself, so it runs if the image has its runtime and the network allows what it needs.
