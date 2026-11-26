# MCP gateway

claude in the VM uses remote MCP servers and claude.ai connectors through an MCP gateway on the host (`mbx mcp-gw`), which forwards the requests with the tokens, so they stay out of the VM. `mbx run` starts it when needed.

## How the VM reaches it

The gateway opens a unix socket for each sandbox, which msb exposes to that sandbox only, at a vsock port. In the VM, socat relays `127.0.0.1:<mcp_gateway.port>` to it, and claude's MCP config points there. So the VM holds no credentials, other sandboxes can't use its servers, and the socket tells the gateway which sandbox a request comes from. A custom image needs `socat` for MCP servers.

## Server definitions

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

## Tokens

- **`mcp_servers`: mbx's own login.** Log in once with `mbx mcp login <server>`. mbx finds the server's OAuth authorization server as MCP clients do (its protected resource metadata, then the authorization server's metadata), registers itself as a client by dynamic client registration unless `client_id` is set, and opens the browser to authorize, with PKCE. The redirect goes to `http://localhost:<callback_port>/callback` (a random port by default; servers whose `client_id` was registered with a fixed redirect need that port). The login is saved in the keyring under service `mbx-mcp-gateway`: account `<server>` holds the login (url, client, expiry), and `<server>:access` and `<server>:refresh` the tokens. The gateway refreshes them when they expire. Changing `url` needs a new login
- **`claude_ai_connectors`: the host claude's claude.ai token.** claude.ai connectors can only be used with the claude.ai login, so the gateway reads the host claude's token (from the keychain on macOS, `~/.claude/.credentials.json` on Linux) only for these. The gateway doesn't refresh it, so when it expires, use the host's claude once (`mbx mcp` shows expiry)

## claude.ai connectors

Connectors get the same names in the VM as on the host (`claude_ai_Slack`), so tool names (`mcp__claude_ai_Slack__...`) and permission settings in `settings.json` match the host's. Tools blocked on claude.ai are removed from the list by the gateway, and calls to them are refused.

## Restarting

Run `mbx mcp-gw restart` after changing a connector's tool permissions on claude.ai, logging in to a server at a new url, or updating mbx.

## stdio servers

Only remote (HTTP) servers go through the gateway. A stdio server in a repository's `.mcp.json` is started by claude in the VM itself, so it runs if the image has its runtime and the network allows what it needs.
