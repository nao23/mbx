# mbx

A CLI that runs a coding agent (or a shell) inside a [microsandbox](https://docs.microsandbox.dev) microVM. Claude Code is the only agent supported so far 😢

- Mounts the current directory into the VM at the same path as on the host. Changes show up on the host immediately
- Sandboxes are disposable. They're removed on exit
- Outbound traffic is limited to the domains on the allowlist
- Secret values (tokens) never enter the VM. The VM gets a placeholder, and a proxy swaps in the real value as it's sent
- A Docker Engine dedicated to the VM is available. Images for other architectures also run, under QEMU
- Claude Code's session history is shared with the host's `~/.claude`, so the host's claude and the sandbox can `--resume` each other's sessions

The config is a single YAML file. microsandbox's own settings are passed to microsandbox as-is; mbx handles only what it adds (sharing with the host, Docker, GitHub, etc.).

## Requirements

- macOS (Apple Silicon) or Linux (with KVM)
- [microsandbox](https://docs.microsandbox.dev) (`msb`; tested with the Homebrew install)
- Go (to install)
- Docker on the host (to build the default image; Lima etc. is fine)
- `git` (for the `git` feature, reading config)

## Installation

```sh
go install github.com/nao23/mbx@latest
```

## Setup

Put these files in the config dir `~/.config/mbx/`:

| File | Contents |
|---|---|
| `<name>.yaml` | Configs, each for the directories it lists (see below). Without one, the defaults alone apply |
| `sandbox-prompt.md` | (Optional) Notes appended to [the built-in prompt](internal/agent/sandbox-prompt.md) given to Claude in the VM, named in `claude.extra_system_prompt` |

Issue a long-lived Claude Code token with `claude setup-token`. By default mbx reads it from the host's `CLAUDE_CODE_OAUTH_TOKEN` environment variable:

```sh
claude setup-token                       # issue a long-lived token
export CLAUDE_CODE_OAUTH_TOKEN='<token>'  # e.g. in your shell profile
```

To keep it out of the environment, store it in the keyring and point `claude.secret_source` at it:

```sh
security add-generic-password -s claude-microsandbox-token -a "$USER" -w '<token>'
```

```yaml
claude:
  secret_source: {keyring: claude-microsandbox-token}
```

By default the VM image is `mbx-default:latest`, built from mbx's built-in [`Dockerfile`](internal/sandbox/Dockerfile) (Ubuntu with Claude Code, Docker Engine, QEMU, uv, etc.). `mbx run` builds it when it's missing or outdated. To update Claude Code in it, run `mbx build --default -- --no-cache`.

### Using your own image

Write a Dockerfile, and build it with `mbx build`: like `docker build`, but it also loads the image into microsandbox.

```sh
mbx build -t my-sandbox:latest .
```

```yaml
microsandbox_config:
  image: my-sandbox:latest
```

The image needs what the agent and the enabled features run (Ubuntu package names in parentheses):

- `claude` agent: `claude`
- `docker`: `dockerd` and the docker CLI (`docker.io`), `iptables`, `dnsmasq` (`dnsmasq-base`)
- `docker.emulation`: QEMU user mode with its binfmt entries (`qemu-user`, `qemu-user-binfmt`)
- `github`: `git`, and `gh` to use the GitHub CLI
- `uv`: `uv`
- MCP servers: `socat`

An image named in `microsandbox_config.image` that mbx didn't build is pulled by microsandbox from its registry.

## Usage

```sh
mbx run claude                   # start claude
mbx run claude -- -p "..."       # pass arguments to claude
mbx run shell                    # start a shell (bash by default)
mbx run shell -- make test       # start any command
mbx run claude --keep            # keep the sandbox after exit
mbx run claude --dry-run         # only show the steps (runs nothing, fetches no secrets)
mbx run claude --show-config     # only show the config used (which file, and why), with the options applied
mbx run claude --config ~/work.yaml  # use this config file (any command takes --config)
```

| Command | Description |
|---|---|
| `mbx run claude\|shell [OPTIONS] [-- ARGS...]` | Create a sandbox and start the agent in it. If a sandbox with the same name exists, connect to it |
| `mbx ls` | List the sandboxes mbx created |
| `mbx stop NAME...` | Stop sandboxes (for those kept with `--keep`) |
| `mbx rm NAME...` | Remove sandboxes |
| `mbx secret rotate [OPTIONS]` | Fetch the sandboxes' secrets again and swap in the new values, without a restart |
| `mbx build -t IMAGE [-f DOCKERFILE] [CONTEXT]` | Build an image for the VMs and load it into microsandbox (`--default` for the default image) |
| `mbx mcp [status\|login\|logout]` | mbx's logins to MCP servers |
| `mbx mcp-gw [status\|start\|stop\|restart]` | Manage the MCP gateway |
| `mbx completion zsh\|bash\|fish` | Print a shell completion script |

`mbx <command> --help` describes each command and its options.

## Configuration

Config files are `~/.config/mbx/*.yaml` (the config dir can be changed with the `MBX_CONFIG_DIR` environment variable), each used in the directories it lists (see [Configs per directory](#configs-per-directory)). Anything left out takes the built-in default ([`internal/config/defaults.yaml`](internal/config/defaults.yaml), which describes every setting). The minimal config is `version: 1` and `directories`.

The config has a part passed to microsandbox as-is, and mbx's extensions.

- **`microsandbox_config:`**: microsandbox's config file format. Passed to `msb create --conf` without being interpreted. See microsandbox's docs for its keys
- **`microsandbox_flags:`**: things the config file format can't express (`--root-disk`, `--max-memory`, `--cpu-placement`, etc.), passed as-is as `msb create` flags
- **Everything else**: mbx's extensions

<details>
<summary>Example</summary>

```yaml
version: 1
directories: ["~"]              # anywhere under the home dir no other file covers

# --- Passed to microsandbox as-is ---
microsandbox_config:            # msb create --conf
  image: mbx-default:latest     # the default image (built by mbx); other images are pulled from their registry
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

# --- mbx's extensions ---
command: [bash]                 # the command mbx run shell starts
keep: false                     # don't remove the sandbox on exit (same as run --keep)
setup: |                        # run in the VM on every start, before the agent (write $VAR, not ${VAR})
  uv sync

claude:                         # used only by mbx run (claude), not mbx run shell
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
  emulation: true               # run other architectures under QEMU
  ecr:                          # log docker in the VM in to ECR
    enabled: false
    registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com
    secret_source:              # defaults to the host's ECR_TOKEN
      command: aws ecr get-login-password --region ap-northeast-1 --profile my-profile

git:                            # copy the host's git settings when the sandbox is created
  host_user: true               # [user] of the global gitconfig (name, email, ...)
  host_ignore: true             # the global ignore file (core.excludesfile, else ~/.config/git/ignore)

github:                         # make a GitHub token available
  enabled: false
  secret_source: {command: gh auth token}   # defaults to the host's GH_TOKEN
  fast_ssh_keyscan: false       # let SSH to GitHub (ssh-keyscan, etc.) through without waiting
  allow: [actions_storage]      # more GitHub hosts to allow, by group (actions_storage: Actions artifacts and logs)

uv:
  vm_venv: true                 # uv in the VM keeps project virtualenvs in the VM (not .venv)

network_extras:
  tls_intercept: []             # domains to intercept TLS for (secret destinations are added automatically)

secret_sources:                 # where the values of secrets in microsandbox_config.secrets come from
  MY_TOKEN: {keyring: my-token}      # one of keyring / command / env
```

</details>

### Configs per directory

For directories that need a different config, put another config file in the config dir:

```yaml
# ~/.config/mbx/work.yaml
directories: [~/src/github.com/example]   # these directories and everything under them
docker:
  ecr: {enabled: true, registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com}
github: {enabled: true}
```

- The file whose `directories` has the working directory, or the deepest of its parents, is used. So a file with `directories: ["~"]` is used where no file lists a deeper directory. Where no file matches, the defaults alone apply
- Only that one file is used. Config files aren't layered over each other, so a file has everything that differs from the defaults
- `--config FILE` uses that file instead, wherever it is

### Layering

Layered in this order: defaults → the config file → command-line options. Maps are merged key by key; values and lists are replaced, except `microsandbox_config.network.allow`, which is appended to. `mbx run claude --show-config` shows the result.

## Secrets

All secret values are handled the same way. The value is fetched on the host, and the VM gets a placeholder. When a program in the VM sends the placeholder, the proxy swaps in the real value, only for traffic to the allowed destination.

Where a value comes from is written the same way everywhere, in `secret_source` / `secret_sources`:

| Form | Source |
|---|---|
| `{keyring: NAME}` | The item with service name NAME and account the login user in the keyring: the macOS keychain, or the Secret Service on Linux |
| `{command: CMD}` | The output of running CMD in a shell |
| `{env: VAR}` | The host environment variable VAR |

### Built-in (claude, ECR, GitHub)

The environment variable in the VM and the destination are fixed; only the source can be changed with `secret_source`. The destination and the other hosts the feature needs are allowed automatically ([docs/network.md](docs/network.md)).

| Setting | Environment variable in the VM → destination |
|---|---|
| `claude` | `CLAUDE_CODE_OAUTH_TOKEN` → `api.anthropic.com` |
| `docker.ecr` | `ECR_TOKEN` → `registry` |
| `github` | `GH_TOKEN` → `github.com`, `api.github.com` |

### Your own

Put the name and destination in `microsandbox_config.secrets` (microsandbox's own format), and the source in `secret_sources`:

```yaml
microsandbox_config:
  secrets:
    MY_TOKEN:
      allow: [api.example.com]    # destination

secret_sources:
  MY_TOKEN: {keyring: my-token}
```

## MCP servers

claude in the VM can use remote (HTTP) MCP servers and claude.ai connectors. Tokens stay out of the VM: an MCP gateway on the host (`mbx mcp-gw`, started by `mbx run` when needed) forwards the requests with the tokens.

```yaml
claude:
  mcp_servers: [example, sentry]   # MCP servers, with mbx's own login
  claude_ai_connectors: [Slack]    # claude.ai connectors (display names), with the host claude's token

mcp_gateway:
  servers:                         # needed only for servers the host's claude doesn't have, or to override it
    sentry:
      url: https://mcp.example.com/mcp
      client_id: abc123            # for a server without dynamic client registration
      callback_port: 13579         # the redirect port registered for client_id
```

- **`mcp_servers`**: defined by `mcp_gateway.servers`, or else the host claude's user-scope server of the same name (`claude mcp add --scope user`). Log in once with `mbx mcp login <server>`; the gateway refreshes the tokens
- **`claude_ai_connectors`**: use the host claude's claude.ai login. When it expires, use the host's claude once

`mbx mcp` shows the logins and their expiry. See [docs/mcp-gateway.md](docs/mcp-gateway.md) for how the gateway works, server definitions and logins.

## How it works

`mbx run claude --dry-run` shows the actual steps. For claude, it roughly does the following:

1. [Host] Fetch secret values (kept only in this process's environment and inherited by `msb`)
2. [Host] Create the shared parts of `~/.claude` (`projects/`, `file-history/`, `history.jsonl`) if they're missing. They exist once the host's claude has been used, so this is usually skipped
3. [Host] Write the config files for `msb create`
   - `sandbox.yaml`: the `microsandbox_config:` section (except `network`), plus what mbx adds:
     - `scripts`: the VM setup script `mbx-setup`
     - `patches`: claude's initial `.claude.json` (onboarding done, working directory trusted), a copy of the org settings cache, and the host's git `[user]` and global ignore file. Written once, at creation
   - `network.yaml`: `microsandbox_config.network` plus the entries mbx adds
4. [Host → VM] Create the sandbox with `msb create`. `microsandbox_flags`, mounts and secrets are passed as flags
5. [VM] Set up the VM with `msb exec <name> -- mbx-setup`. It's assembled from the enabled features (registering QEMU, starting Docker Engine, wrapping uv, logging in to ECR, rewriting GitHub URLs for git)
6. [VM] Start claude with `msb exec -t`
7. [Host] Remove the sandbox on exit

### Network

Traffic to domains not on the allowlist is blocked, and TLS is intercepted only for secret destinations. See [docs/network.md](docs/network.md) for the details and the hosts mbx adds.

### Docker in the VM

- `/var/lib/docker` is an ext4 disk dedicated to the sandbox, since containers can't be created on the root filesystem (overlay)
- Container DNS goes through dnsmasq in the VM (`172.17.0.1`). mbx starts dockerd with `--dns 172.17.0.1` to set this up (if the image's `daemon.json` has `dns`, that's used instead). Some routers answer a non-EDNS query with more than 512 bytes; microsandbox's DNS can't receive it all and returns SERVFAIL, and musl (Alpine-based) containers fail to resolve names. dnsmasq keeps answers small to prevent this
- Binaries for other architectures run under QEMU (user mode). Speed is about a third to a quarter of native

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
- Traffic is allowed by domain, IP and port only. microsandbox can't restrict it by HTTP method or path (e.g. allow GET but not POST to an API)
- Secrets can't be added to a running sandbox later (their values can be swapped)
- Host tools and files outside the mounted directories aren't available in the VM
- The MCP gateway supports only remote (HTTP) MCP servers, not stdio ones
- Different projects with the same directory name get the same sandbox name (avoid it with `--name`)
- Names starting with `mbx-` can't be used in `microsandbox_config.scripts` (they're for mbx's built-in scripts)

## License

[MIT](LICENSE)
