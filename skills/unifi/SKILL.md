---
name: unifi
description: Query and manage a UniFi network (gateway, switches, access points, clients, networks/VLANs, WiFi, firewall, DNS, ACLs, vouchers, VPN, WANs) with the `unifi` CLI. Use whenever the user asks about their home/office network, UniFi devices, connected clients, or wants to change UniFi configuration.
---

# UniFi CLI

`unifi` wraps the official UniFi Network integration API. Every API operation is a command, generated from the API's OpenAPI spec. Output is JSON on stdout, errors go to stderr, and exit codes are 0 ok, 1 runtime/API error, 2 usage error (including a destructive command run without `--yes`).

## Discover before guessing

- `unifi commands`: every command with its args and a one-line summary. Start here.
- `unifi <command> --help`: flags, filterable fields, the full request body schema, and the HTTP method/path.
- `unifi schema <name> [--depth N]`: any schema from the spec (names match ignoring case, spaces and dashes). Help output points at these for deeper `$ref`s.

Commands are `unifi <resource...> <verb> [ids...] [flags]`, for example:

```
unifi sites list
unifi devices list
unifi devices get <device-id|name|mac|ip>
unifi devices statistics <device-id>
unifi clients list --filter "type.eq('WIRED')"
unifi networks list --fields id,name,vlanId
unifi firewall policies ordering get --source-firewall-zone-id <id> --destination-firewall-zone-id <id>
```

ID arguments take a UUID, or a name (any case), MAC (any separator) or IP where the resource has one: `unifi devices get office-ap`, `unifi clients get aa:bb:cc:dd:ee:ff`. A non-UUID costs one extra list request and fails if it matches zero or several items (the error lists the candidates' UUIDs). Prefer UUIDs from earlier output when you already have them, and always use the UUID for destructive commands once you've confirmed which item is meant. Flags can come before or after positional args.

## Sites

Most commands are site-scoped. The site defaults to the configured one, or the only site. Override it with `--site <uuid|internal-reference|name>`, e.g. `--site default`.

## Keep output small

- `--fields a,b.c` keeps only those fields. On a page (`{data: [...], totalCount}`) it applies to each item and keeps the paging metadata. Arrays along a path are kept, e.g. `--fields id,interfaces.ports.idx`.
- List commands return one page (default 25, max 200 via `--limit`). Check `totalCount`; use `--all` to fetch every page in one call.
- `--filter` narrows lists server-side. The filterable fields for each list are in its `--help`.

## Filter syntax

`unifi help filter` prints the full reference from the API spec. In short:

- Property: `<field>.<fn>(<args>)`, e.g. `name.eq('Office')`, `ipAddress.isNotNull()`, `state.in('ONLINE', 'OFFLINE')`, `name.like('guest*')`
- `like` patterns: `*` matches any run of characters and `.` matches exactly one character; escape them with `\`.
- Combine: `and(a, b, ...)`, `or(a, b, ...)`, `not(a)`
- Functions: `isNull isNotNull eq ne gt ge lt le like in notIn`; for set-typed fields such as `features`, use `isEmpty contains containsAny containsAll containsExactly`. Each list's help shows which functions apply to which field.
- Values: strings in single quotes (escape `'` as `''`); numbers, `true`/`false`, UUIDs and ISO-8601 timestamps bare; sets as `[1, 2]`

Quote the whole filter for the shell: `--filter "and(type.eq('WIRED'), ipAddress.isNotNull())"`.

## Changing things

1. Read the command's `--help` for the body schema. Bodies with variants show `x-variants`, keyed by the discriminator value (e.g. `A_RECORD`). Send exactly one variant's properties, with the discriminator field set to that key.
2. For updates (PUT), `get` the object first and send back the full modified object. PUT replaces the whole object.
3. Pass the body with `--data '<json>'`, `--data @file.json`, or `--data -` (stdin).
4. Preview with `--dry-run`: it prints the method, URL and body without sending anything.
5. Destructive commands (deletes, device/port/client actions such as restart, power-cycle or block, and unadopt) refuse to run without `--yes`. **Always confirm with the user before adding `--yes`**, and state exactly what will happen, e.g. "restart the U7 Pro in the office".

Creating or updating networks, WiFi, firewall policies, ACLs and DNS also changes live network behavior. Confirm those with the user too, even though they don't need `--yes`.

## Gaps

If an endpoint exists on the console but not in `unifi commands` (newer firmware), use the raw form:
`unifi api GET /v1/sites/{siteId}/<path> [--query k=v] [--data ...]`. `{siteId}` is filled in automatically, and non-GET requests need `--yes`.

## Troubleshooting

- `no host configured` / `no API key configured`: the user must run `unifi config init --host <ip> --api-key-cmd '<cmd>'` (the API key comes from UniFi Network → Settings → Control Plane → Integrations). Don't invent keys.
- `certificate fingerprint mismatch`: the console certificate changed. Ask the user before re-running `unifi config init` to re-pin it.
- `HTTP 401`: bad or revoked API key. `HTTP 404`: wrong ID or site. `HTTP 400`: the body didn't match the schema; re-read `--help`.
- `unifi config show` prints the active profile, with the key redacted.
