# unifi-cli

A small, dependency-free Go CLI for the official [UniFi Network integration API](https://developer.ui.com/network/), built mainly for AI agents to drive (see [skills/unifi/SKILL.md](skills/unifi/SKILL.md)).

- **Full coverage, generated:** every operation in the OpenAPI spec is a command. The spec is committed and embedded, and `internal/cmd/gen` turns it into a plain Go table ([internal/spec/ops_gen.go](internal/spec/ops_gen.go)). New firmware means fetch, regenerate, review the diff.
- **Built for agents:** JSON output, `--fields` projection, `--all` pagination, `--dry-run`, `--data` checked against the spec before sending, request body schemas in `--help` (polymorphic bodies flattened into concrete variants), and `--yes` required for destructive operations.
- **Standard library only.** No third-party modules.

## Install

Download an archive for your platform (macOS or Linux, arm64 or amd64) from [Releases](https://github.com/nickzelei/unifi-cli/releases), then check it and put `unifi` on your `PATH`:

```bash
shasum -a 256 -c checksums.txt --ignore-missing
gh attestation verify unifi_*.tar.gz --repo nickzelei/unifi-cli   # optional: built by this repo's release workflow
tar -xzf unifi_*.tar.gz && mv unifi_*/unifi ~/.local/bin/
```

Or build from source with `go install github.com/nickzelei/unifi-cli/cmd/unifi@latest`, or `mise run install` in a checkout.

### Install with an AI agent

Paste this into Claude Code (or another coding agent):

```text
Install the unifi CLI from https://github.com/nickzelei/unifi-cli and set it up for me.
1. Download the latest release's unifi_<version>_<os>_<arch>.tar.gz for this machine
   (darwin or linux, arm64 or amd64) and checksums.txt from
   https://github.com/nickzelei/unifi-cli/releases/latest.
2. Verify it with `shasum -a 256 -c checksums.txt --ignore-missing` and stop if it fails.
3. Extract it and put `unifi` in a directory on my PATH (e.g. ~/.local/bin); check `unifi version`.
4. Run `unifi skill install` so you know how to use it.
5. Help me run `unifi config init`. I need an API key from UniFi Network → Settings →
   Control Plane → Integrations. Ask for my console's address and prefer
   `--api-key-cmd` with my password manager (e.g. `op read ...`) over pasting the key;
   never ask me to paste the key into this chat. I can also run `unifi config init`
   myself with no flags to be prompted.
6. Finish with `unifi doctor` and explain anything that isn't ok.
```

## Configure

Create an API key in UniFi Network → Settings → Control Plane → Integrations, then:

```bash
unifi config init --host 192.168.1.1 --api-key-cmd "op read op://Private/UniFi/credential"
```

Run `unifi config init` with no flags at a terminal to be prompted for each setting instead.

`config init`:

- connects to the console;
- pins its self-signed certificate (SHA-256 of the leaf), or skips pinning if the certificate is publicly trusted;
- checks the key against `/v1/info`;
- stores the site's UUID;
- writes `~/.config/unifi/config.json` (mode 0600).

`unifi doctor` re-checks all of this later (config, TLS pin and expiry, key, API, site) and compares the console's Network version with the CLI's embedded spec. Commands also warn on stderr, at most once a day, when the console is newer than the spec; the console's version is cached under the OS cache dir (`UNIFI_NO_VERSION_CHECK=1` turns this off).

`--api-key` stores the key inline instead. Every setting can be overridden with an env var: `UNIFI_PROFILE`, `UNIFI_HOST`, `UNIFI_BASE_URL`, `UNIFI_API_KEY`, `UNIFI_SITE`, `UNIFI_TLS_FINGERPRINT`, `UNIFI_INSECURE`, `UNIFI_READ_ONLY`. Multiple consoles go in separate `--profile`s.

### Read-only

`unifi config init --read-only` (kept on re-init; `--read-only=false` clears it), or `UNIFI_READ_ONLY=1` for one shell or agent, makes the CLI send only GET requests. Anything else is refused before it's sent, even with `--yes`; `--dry-run` still works. The env var can only turn read-only on, and unlike the profile setting it covers every profile, so set it in an agent's environment when other profiles exist. It's a guardrail in the CLI, not a permission: for a hard limit, use an API key that can only read, if your console supports that.

## Use

```bash
unifi commands                                   # everything available
unifi devices list --fields id,name,model,state
unifi clients list --all --filter "type.eq('WIRELESS')"
unifi dns policies create --help                 # body schema + variants
unifi dns policies create --dry-run --data '{"type":"A_RECORD","enabled":true,"domain":"nas.lan","ipv4Address":"192.168.1.20","ttlSeconds":300}'
unifi devices get office-ap                      # IDs: UUID, or name / MAC / IP
unifi clients list -o table                      # human-readable; UNIFI_OUTPUT=table makes it the default
unifi devices action <id> --data '{"action":"RESTART"}' --yes
unifi api GET /v1/sites/{siteId}/wans            # raw escape hatch
```

ID arguments take a UUID as-is. Anything else is looked up in the matching list (`devices get x` searches `devices list`) by name (any case), MAC (any separator) or IP, costing one extra request; zero or multiple matches is an error that lists the candidates.

## Agent skill

```bash
unifi skill install                 # writes the bundled skill to ~/.claude/skills/unifi
unifi skill install --dir <dir>     # another agent's skills directory
mise run skill:install              # in a checkout: symlink skills/unifi instead, to track edits
```

Re-run `unifi skill install` after upgrading to update the skill. It leaves a symlinked install alone unless you pass `--force`.

## Development

| Task | What it does |
|-|-|
| `mise run build` | build `bin/unifi` |
| `mise run test` / `lint` | tests; golangci-lint (vet, staticcheck, gofmt, goimports, and more) + deadcode (`lint:fix` applies fixes) |
| `mise run check` | lint, test, and verify `ops_gen.go` matches the spec |
| `mise run spec:update` | fetch the spec from your console, regenerate |
| `mise run spec:fetch -- --url <url>` | fetch from a URL instead, e.g. `https://developer.ui.com/network/v10.6.106/openapi.json` |
| `mise run release -- patch` | tag a release: checks main is clean, pushed and green, then pushes a signed `vX.Y.Z` tag; `-n` for a dry run, `minor`/`major`/`X.Y.Z[-pre]` also work |
| `mise run release:build` | cross-compile release archives and `checksums.txt` into `dist/` (the release workflow runs this for signed `v*` tags) |
| `mise run gen` | regenerate `ops_gen.go` from the committed spec |

Command names come from the API paths: `/v1/sites/{siteId}/dns/policies/{id}` becomes `dns policies get|update|delete <dns-policy-id>`. Operations those rules can't name (actions, sub-resources) need an explicit entry in `overrides` in [internal/cmd/gen/main.go](internal/cmd/gen/main.go). Generation fails loudly on a missing override or a name collision, so new endpoints always get a deliberate name.

Planned work is tracked in [GitHub issues](https://github.com/nickzelei/unifi-cli/issues).

### Layout

```
cmd/unifi/          entrypoint
internal/cli/       commands, flags, help, output
internal/spec/      embedded openapi.json, generated ops_gen.go, schema rendering
internal/unifi/     config, HTTP client, TLS pinning, site resolution
internal/cmd/gen/   spec fetcher + op table generator
skills/unifi/       agent skill (embedded for `unifi skill install`)
```

## License

[MIT](LICENSE)
