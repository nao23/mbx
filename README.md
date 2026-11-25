# msbx

A CLI that runs a coding agent (or a shell) inside a [microsandbox](https://docs.microsandbox.dev) microVM. Claude Code is the only agent supported so far 😢

- Mounts the current directory into the VM at the same path as on the host. Changes show up on the host immediately
- Sandboxes are disposable. They're removed on exit
- Outbound traffic is limited to the domains on the allowlist
- Secret values (tokens) never enter the VM. The VM gets a placeholder, and a proxy swaps in the real value as it's sent
- A Docker Engine dedicated to the VM is available. amd64 images also run, under QEMU
- Claude Code's session history is shared with the host's `~/.claude`, so the host's claude and the sandbox can `--resume` each other's sessions

The config is a single YAML file. microsandbox's own settings are passed to microsandbox as-is; msbx handles only what it adds (sharing with the host, Docker, GitHub, etc.).

## Requirements

- macOS (Apple Silicon)
- [microsandbox](https://docs.microsandbox.dev) (`msb`; tested with the Homebrew install)
- Go (to install)
- Docker on the host (to build the default image; Lima etc. is fine)
- `git` (for the `git` feature, reading config)

## Installation

```sh
go install github.com/nao23/msbx@latest
```

## Setup

Put these files in the config dir `~/.config/msbx/`:

| File | Contents |
|---|---|
| `config.yaml` | The config (see below) |
| `sandbox-prompt.md` | (Optional) Notes appended to the system prompt given to Claude in the VM. msbx always passes a description of the VM environment ([the built-in prompt](internal/agent/sandbox-prompt.md)) and appends the file named in `claude.extra_system_prompt` after it |

Issue a long-lived Claude Code token with `claude setup-token`. By default msbx reads it from the host's `CLAUDE_CODE_OAUTH_TOKEN` environment variable:

```sh
claude setup-token                       # issue a long-lived token
export CLAUDE_CODE_OAUTH_TOKEN='<token>'  # e.g. in your shell profile
```

To keep it out of the environment, store it in the keychain and point `claude.secret_source` at it:

```sh
security add-generic-password -s claude-microsandbox-token -a "$USER" -w '<token>'
```

```yaml
claude:
  secret_source: {keychain: claude-microsandbox-token}
```

By default the VM image is `msbx-default:latest`, built from msbx's built-in [`Dockerfile`](internal/sandbox/Dockerfile) (Ubuntu with Claude Code, Docker Engine, QEMU, uv, etc.). `msbx run` builds it automatically when it's missing, and rebuilds it when an msbx update has changed the built-in `Dockerfile` (detected by a hash recorded in an image label). The image is built for arm64 with the host's Docker and loaded into microsandbox. Claude Code is the latest version at build time, so to update it, rebuild with `msbx rebuild`.

If `microsandbox_config.image` names another image (`ubuntu:24.04`, `ghcr.io/...`, etc.), msbx doesn't build anything; microsandbox pulls it from its registry. msbx features (Docker, the uv wrapper, etc.) don't work if the image lacks what they rely on.

### Using your own image

Write a Dockerfile, build it for arm64 (as the VMs are) with the host's Docker, and load it into microsandbox, as msbx does for the default image:

```sh
docker build --platform linux/arm64 -t my-sandbox:latest .
docker save my-sandbox:latest | msb load -t my-sandbox:latest
```

```yaml
microsandbox_config:
  image: my-sandbox:latest
```

The image needs what the agent and the enabled features run (Ubuntu package names in parentheses):

- `claude` agent: `claude`
- `docker`: `dockerd` and the docker CLI (`docker.io`), `iptables`, `dnsmasq` (`dnsmasq-base`)
- `docker.emulation`: QEMU user mode with its binfmt entries (`qemu-user`, `qemu-user-binfmt`)
- `docker.ecr`: what `docker` needs
- `github`: `git`, and `gh` to use the GitHub CLI
- `uv`: `uv`
- MCP servers: `socat`

The built-in [`Dockerfile`](internal/sandbox/Dockerfile) is a starting point. msbx doesn't rebuild your image, so run the two commands again to update it.

## Usage

```sh
msbx run                         # start claude (the agent in the config is the default)
msbx run claude -- -p "..."      # pass arguments to claude
msbx run shell                   # start a shell (bash by default)
msbx run shell -- make test      # start any command
msbx run --keep                  # keep the sandbox after exit
msbx run --dry-run               # only show the steps (runs nothing, fetches no secrets)
msbx run --show-config           # only show the merged config, with the options applied
```

| Command | Description |
|---|---|
| `msbx run [claude\|shell] [OPTIONS] [-- ARGS...]` | Create a sandbox and start the agent in it. If a sandbox with the same name exists, connect to it |
| `msbx ls` | List the sandboxes msbx created |
| `msbx stop NAME...` | Stop sandboxes (for those kept with `--keep`) |
| `msbx rm NAME...` | Remove sandboxes. Session history shared with the host is kept |
| `msbx rebuild` | Rebuild the default image (`msbx-default:latest`). Use it to update Claude Code |
| `msbx mcp [status\|login\|logout]` | msbx's logins to MCP servers: their state and where the credentials are kept (see below) |
| `msbx mcp-gw [status\|start\|stop\|restart]` | Manage the MCP gateway (see below) |
| `msbx completion zsh\|bash\|fish` | Print a shell completion script |

`msbx <command> --help` describes each command and its options.

Only the `run` that created a sandbox removes it on exit. If you connect to a running claude sandbox with `msbx run shell --name claude-<dir name>`, closing the shell leaves the claude sandbox running.

## Configuration

The config file is `~/.config/msbx/config.yaml` (override with the `MSBX_CONFIG` environment variable). Anything left out takes the built-in default ([`internal/config/defaults.yaml`](internal/config/defaults.yaml)). The minimal config is just `version: 1`.

The config has a part passed to microsandbox as-is, and msbx's extensions.

- **`microsandbox_config:`**: microsandbox's config file format. Passed to `msb create --conf` without being interpreted. See microsandbox's docs for its keys
- **`microsandbox_flags:`**: things the config file format can't express (`--root-disk`, `--max-memory`, `--cpu-placement`, etc.), passed as-is as `msb create` flags
- **Everything else**: msbx's extensions

<details>
<summary>Example</summary>

```yaml
version: 1

# --- Passed to microsandbox as-is ---
microsandbox_config:            # msb create --conf
  image: msbx-default:latest    # the default image (built by msbx); other images are pulled from their registry
  cpus: 2
  memory: 4G
  env:
    TZ: Asia/Tokyo              # environment variables in the VM (time zone, etc.; the default is UTC)
  network:
    allow:                      # allowed domains (those the agent and enabled features need are added automatically)
      - github.com
      - pypi.org
      - "*.docker.io"

microsandbox_flags:             # msb create flags (flag name: value)
  root-disk: 8G                 # → --root-disk 8G
  # max-memory: 8G
  # some-list: [a, b]           # → --some-list a --some-list b (a list repeats the flag)
  # some-switch: true           # → --some-switch (true gives a bare flag; false and null are omitted)

# --- msbx's extensions ---
agent: claude                   # what run starts when not given (claude / shell)
command: [bash]                 # the command shell starts
keep: false                     # don't remove the sandbox on exit (same as run --keep)

claude:                         # used only when the agent is claude
  secret_source: {env: CLAUDE_CODE_OAUTH_TOKEN}   # where the value of CLAUDE_CODE_OAUTH_TOKEN comes from (default: the host env var)
  extra_system_prompt: sandbox-prompt.md   # a file appended after the built-in prompt (the VM environment description)
  config_dir: /root/.claude-state   # CLAUDE_CONFIG_DIR in the VM
  mcp_servers: []               # MCP servers in mcp_gateway.servers, through the MCP gateway (see below)
  claude_ai_connectors: []      # claude.ai connectors, through the MCP gateway with the host claude's token (see below)
  host_share:
    sessions: true              # share session history with the host (read-write)
    config: true                # share CLAUDE.md, settings.json, etc. (read-only)
    skills: []                  # skills in ~/.claude/skills to share (read-only), by name, or ["*"] for all

docker:
  enabled: true
  disk: 20G                     # disk for Docker (removed with the sandbox)
  emulation: true               # run amd64 etc. under QEMU
  ecr:                          # log docker in the VM in to ECR (run --ecr)
    enabled: false
    registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com
    secret_source:              # defaults to the host's ECR_TOKEN
      command: aws ecr get-login-password --region ap-northeast-1 --profile my-profile

git:                            # copy the host's git settings when the sandbox is created
  host_user: true               # [user] of the global gitconfig (name, email, ...)
  host_ignore: true             # the global ignore file (core.excludesfile, else ~/.config/git/ignore)

github:                         # make a GitHub token available (run --github)
  enabled: false
  secret_source: {command: gh auth token}   # defaults to the host's GH_TOKEN
  fast_ssh_keyscan: false       # let SSH to GitHub (ssh-keyscan, etc.) through without waiting
  allow: [actions_storage]      # more GitHub hosts to allow, by group (see below)

uv:
  vm_venv: true                 # uv in the VM keeps project virtualenvs in the VM (not .venv)

network_extras:
  tls_intercept: []             # domains to intercept TLS for (secret destinations are added automatically)

secret_sources:                 # where the values of secrets in microsandbox_config.secrets come from
  MY_TOKEN: {keychain: my-token}      # one of keychain / command / env

projects:                       # overrides per working directory
  ~/src/github.com/example/repo:
    docker:
      ecr: {enabled: true}
    github: {enabled: true}
    microsandbox_config:
      memory: 8G
```

</details>

### Layering

Layered in this order: defaults → the config file → the `projects:` entries matching the working directory → command-line options.

- A `projects:` entry applies when it names the working directory or one of its parents. Shallower ones are layered first
- Maps are merged key by key. Values and lists are replaced by the later one
- Only `microsandbox_config.network.allow` is appended to instead of replaced
- `secret_source` (and each entry of `secret_sources`) is replaced whole instead of merged key by key, so `keychain` and `command` don't get mixed

`msbx run --show-config` shows the final config.

## Secrets

All secret values are handled the same way. The value is fetched on the host and kept only in `msb`'s environment, and the VM gets a placeholder. When a program in the VM sends the placeholder, the proxy swaps in the real value, only for traffic to the allowed destination.

Where a value comes from is written the same way everywhere, in `secret_source` / `secret_sources`.

| Form | Source |
|---|---|
| `{keychain: NAME}` | The macOS keychain item with service name NAME and account the login user (as `security add-generic-password -s NAME -a "$USER" -w` makes) |
| `{command: CMD}` | The output of running CMD in a shell |
| `{env: VAR}` | The host environment variable VAR |

`msbx run` shows each secret as it fetches it (its name, source and destination), so a keychain dialog or a login prompt from a command shows what it's for. If fetching fails and msbx is running in a terminal, it asks for the value by hand: the input isn't echoed, is used for that run only, and an empty input aborts with the original error.

### Built-in (claude, ECR, GitHub)

The environment variable in the VM and the destination are fixed; only the source can be changed with `secret_source`. The destination is automatically added to the allowlist and to the TLS interception targets.

| Setting | Environment variable in the VM → destination | What else it does |
|---|---|---|
| `claude` | `CLAUDE_CODE_OAUTH_TOKEN` → `api.anthropic.com` | Also allows `platform.claude.com` |
| `docker.ecr` | `ECR_TOKEN` → `registry` | Also allows the S3 bucket holding image layers (`prod-<region>-starport-layer-bucket.s3.<region>.amazonaws.com`). Logs `docker` in the VM in to ECR |
| `github` | `GH_TOKEN` → `github.com`, `api.github.com` | Lets `git` and `gh` in the VM use the token. Also allows `codeload.github.com`, `*.githubusercontent.com` and `ghcr.io`. Makes git in the VM rewrite `git@github.com:` / `ssh://git@github.com/` to HTTPS with the token |

By default these tokens come from the host's environment variables of the same name (`GH_TOKEN`, `ECR_TOKEN`). To fetch them on each run instead, set e.g. `{command: gh auth token}` or `{command: aws ecr get-login-password --region <region>}`. To keep the VM from pushing to GitHub, give it a read-only fine-grained personal access token.

### Your own

Put the name and destination in `microsandbox_config.secrets` (microsandbox's own format), and the source in `secret_sources`. microsandbox's format has no place for `keychain` and the like, hence the two places.

```yaml
microsandbox_config:
  secrets:
    MY_TOKEN:
      allow: [api.example.com]    # destination (added to the allowlist and TLS interception targets automatically)

secret_sources:
  MY_TOKEN: {keychain: my-token}
```

## MCP servers (the MCP gateway)

claude in the VM can use remote MCP servers and claude.ai connectors. Tokens stay out of the VM: an MCP gateway running on the host (`msbx mcp-gw`) forwards requests with tokens kept in the host's keychain.

Only remote (HTTP) servers are supported; stdio servers aren't. A stdio server in a repository's `.mcp.json` is started by claude in the VM itself, so it runs if the image has its runtime and the network allows what it needs.

```yaml
claude:
  mcp_servers: [example, sentry]   # MCP servers, with msbx's own login
  claude_ai_connectors: [Slack]    # claude.ai connectors (display names), with the host claude's token

mcp_gateway:
  port: 41837
  servers:                         # needed only for servers the host's claude doesn't have, or to override it
    sentry:
      url: https://mcp.example.com/mcp
      client_id: abc123            # for a server without dynamic client registration
      callback_port: 13579         # the redirect port registered for client_id
      client_secret_source: {}     # for a confidential client_id (a secret source, as in secret_sources)
      scope: ""                    # default: what the server asks for
```

A server in `mcp_servers` is defined by its `mcp_gateway.servers` entry, with what the entry leaves out taken from the host claude's user-scope server of the same name (`mcpServers` in `~/.claude.json`, as added with `claude mcp add --scope user`): the `url`, and when the url is the same, `client_id` and `callback_port` (`oauth.clientId` and `oauth.callbackPort`). So a server the host's claude already has, like `example` above, needs no entry. Only the definition is read from there; the host claude's tokens aren't used.

There are two kinds of servers, with tokens from different places:

- **`mcp_servers`: msbx's own login.** Log in once with `msbx mcp login example`. msbx finds the server's OAuth authorization server as MCP clients do (its protected resource metadata, then the authorization server's metadata), registers itself as a client by dynamic client registration unless `client_id` is set, and opens the browser to authorize, with PKCE. The redirect goes to `http://localhost:<callback_port>/callback` (a random port by default; servers whose `client_id` was registered with a fixed redirect need that port). The login is saved in the keychain under service `msbx-mcp-gateway`: account `<server>` holds the login (url, client, expiry), and `<server>:access` and `<server>:refresh` the tokens. The gateway refreshes them when they expire. Changing `url` needs a new login
- **`claude_ai_connectors`: the host claude's claude.ai token.** claude.ai connectors can only be used with the claude.ai login, so the gateway reads the host claude's token from the keychain (`Claude Code-credentials`) only for these. The gateway doesn't refresh it, so when it expires, use the host's claude once (`msbx mcp` shows expiry). Connectors get the same names in the VM as on the host (`claude_ai_Slack`), so tool names (`mcp__claude_ai_Slack__...`) and permission settings in `settings.json` match the host's. Tools blocked on claude.ai are removed from the list by the gateway, and calls to them are refused

`msbx run` starts the gateway when needed. It reaches each sandbox through a unix socket of its own rather than the network, so the VM holds no credentials and other sandboxes can't use its servers. A custom image needs `socat` for MCP servers.

`msbx mcp` shows the logins and their expiry. Run `msbx mcp-gw restart` after changing a connector's tool permissions on claude.ai, logging in to a server at a new url, or updating msbx.

## How it works

`msbx run --dry-run` shows the actual steps. For claude, it roughly does the following:

1. [Host] Fetch secret values (kept only in this process's environment and inherited by `msb`)
2. [Host] Create the shared parts of `~/.claude` (`projects/`, `file-history/`, `history.jsonl`) if they're missing. They exist once the host's claude has been used, so this is usually skipped
3. [Host] Write the config files for `msb create`
   - `sandbox.yaml`: the `microsandbox_config:` section (except `network`), plus what msbx adds:
     - `scripts`: the VM setup script `msbx-setup`
     - `patches`: claude's initial `.claude.json` (onboarding done, working directory trusted), a copy of the org settings cache, and the host's git `[user]` and global ignore file. Written once, at creation
   - `network.yaml`: `microsandbox_config.network` plus the entries msbx adds
4. [Host → VM] Create the sandbox with `msb create`. `microsandbox_flags`, mounts and secrets are passed as flags
5. [VM] Set up the VM with `msb exec <name> -- msbx-setup`. It's assembled from the enabled features (registering QEMU, starting Docker Engine, wrapping uv, logging in to ECR, rewriting GitHub URLs for git)
6. [VM] Start claude with `msb exec -t`
7. [Host] Remove the sandbox on exit

### Network

- Traffic to domains not on the allowlist is blocked
- TLS is intercepted (decrypted) only for secret destinations (`api.anthropic.com`, the ECR registry, `github.com`, etc.). Other allowed domains aren't intercepted (`tls.bypass`), as containers in the VM don't trust microsandbox's CA
- microsandbox replaces lists whole when layering configs, so for `network` msbx adds its entries first and passes it as one config
- A rule with only a domain (`github.com`, etc.) allows all ports. To restrict ports, write e.g. `github.com:tcp:443`
- For connections matching a domain rule, microsandbox waits up to 5 seconds for the first bytes sent (to read the SNI or Host and check the connection is to an allowed domain). Protocols where the server speaks first, like SSH, wait 5 seconds on every connection. Connections allowed by an IP rule don't wait
- `github.fast_ssh_keyscan` fetches GitHub's git IP ranges from its public `api.github.com/meta` and allows port 22 to them as IP rules placed before the domains. SSH to GitHub (`ssh-keyscan` during builds, etc.) then connects without waiting
- `github.allow` adds groups of GitHub hosts. `actions_storage` allows the Actions artifact and log storage hosts (`productionresultssa*.blob.core.windows.net`, for `gh run download` and `gh run view --log`) listed in the same `api.github.com/meta`. They're Azure Blob Storage accounts, so msbx allows only GitHub's, not `*.blob.core.windows.net`
- `github.allow` adds groups of GitHub hosts. `actions_storage` allows the Actions artifact and log storage hosts (`productionresultssa*.blob.core.windows.net`, for `gh run download` and `gh run view --log`) listed in the same `api.github.com/meta`. They're Azure Blob Storage accounts, so msbx allows only GitHub's, not `*.blob.core.windows.net`

### Docker in the VM

- `/var/lib/docker` is an ext4 disk dedicated to the sandbox, since containers can't be created on the root filesystem (overlay)
- Container DNS goes through dnsmasq in the VM (`172.17.0.1`). msbx starts dockerd with `--dns 172.17.0.1` to set this up (if the image's `daemon.json` has `dns`, that's used instead). Some routers answer a non-EDNS query with more than 512 bytes; microsandbox's DNS can't receive it all and returns SERVFAIL, and musl (Alpine-based) containers fail to resolve names. dnsmasq keeps answers small to prevent this
- amd64 binaries run under QEMU (user mode). arm64 ones are unaffected. Speed is about a third to a quarter of native

### Sharing with the host's `~/.claude`

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

## Limitations

- Changes to the allowlist and other settings take effect the next time a sandbox is created
- Secrets can't be added to a running sandbox later (their values can be swapped)
- Host tools and files outside the mounted directories aren't available in the VM
- The MCP gateway supports only remote (HTTP) MCP servers, not stdio ones
- Different projects with the same directory name get the same sandbox name (avoid it with `--name`)
- Names starting with `msbx-` can't be used in `microsandbox_config.scripts` (they're for msbx's built-in scripts)

## License

[MIT](LICENSE)
