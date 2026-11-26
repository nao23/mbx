# Network

How mbx sets up the VM's network on top of microsandbox's. The allowlist is `microsandbox_config.network.allow` in the config.

- Traffic to domains not on the allowlist is blocked
- TLS is intercepted (decrypted) only for secret destinations (`api.anthropic.com`, the ECR registry, `github.com`, etc.). Other allowed domains aren't intercepted (`tls.bypass`), as containers in the VM don't trust microsandbox's CA
- microsandbox replaces lists whole when layering configs, so for `network` mbx adds its entries first and passes it as one config
- A rule with only a domain (`github.com`, etc.) allows all ports. To restrict ports, write e.g. `github.com:tcp:443`
- For connections matching a domain rule, microsandbox waits up to 5 seconds for the first bytes sent (to read the SNI or Host and check the connection is to an allowed domain). Protocols where the server speaks first, like SSH, wait 5 seconds on every connection. Connections allowed by an IP rule don't wait

## Hosts mbx adds

A secret's destination is added to the allowlist and to the TLS interception targets. The agent and the features add the hosts they need:

| Setting | Hosts |
|---|---|
| `claude` | `api.anthropic.com`, `platform.claude.com` |
| `docker.ecr` | The registry, and the S3 bucket holding its image layers (`prod-<region>-starport-layer-bucket.s3.<region>.amazonaws.com`) |
| `github` | `github.com`, `api.github.com`, `codeload.github.com`, `*.githubusercontent.com`, `ghcr.io` |

## GitHub

- `github.fast_ssh_keyscan` fetches GitHub's git IP ranges from its public `api.github.com/meta` and allows port 22 to them as IP rules placed before the domains. SSH to GitHub (`ssh-keyscan` during builds, etc.) then connects without waiting
- `github.allow` adds groups of GitHub hosts. `actions_storage` allows the Actions artifact and log storage hosts (`productionresultssa*.blob.core.windows.net`, for `gh run download` and `gh run view --log`) listed in the same `api.github.com/meta`. They're Azure Blob Storage accounts, so mbx allows only GitHub's, not `*.blob.core.windows.net`
- git in the VM rewrites `git@github.com:` / `ssh://git@github.com/` to HTTPS, so it uses the token instead of SSH
