# hermai-cli

**English** · [繁體中文](./README.zh-Hant.md) · [简体中文](./README.zh-Hans.md)

> Discover, contribute, and call structured website APIs from your terminal.

`hermai` is the open-source CLI for the [Hermai registry](https://hermai.ai) — a community catalog of website API schemas for AI agents. Probe a site to document its endpoints, push the schema to the catalog, or pull existing schemas your agent can call.

```bash
go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest
```

Homebrew, npm, and prebuilt binaries coming soon.

## Agent skills

Running in Claude Code, Codex, Cursor, or another agent? Install the skills so the agent knows how to use this CLI:

```bash
npx skills add hermai-ai/hermai-skills
```

- **`hermai`** — call the registry and consume schemas.
- **`hermai-contribute`** — use the discovery toolkit to add a site.

Repo: [hermai-ai/hermai-skills](https://github.com/hermai-ai/hermai-skills).

## Registry

```bash
hermai registry login                         # GitHub OAuth, stores API key
hermai registry list                          # browse the catalog
hermai registry pull <site> --intent "..."    # download a schema
hermai registry push schema.json              # contribute a schema
```

## Discovery toolkit

Deterministic subcommands for composing a new schema. No LLM key — each prints JSON the next step can consume.

```bash
hermai detect <url>                          # platform + anti-bot classification
hermai wellknown <domain>                    # robots, sitemap, RSS, GraphQL
hermai probe --body <url> | hermai extract   # 13 embedded-data patterns
hermai intercept <url>                       # capture XHR in a browser
hermai introspect <graphql-url>              # GraphQL schema
hermai replay request.json                   # replay a captured request
hermai session bootstrap <site>              # warm a fresh browser session
hermai session import <site>                 # import cookies from your current browser
```

### Import your existing browser session

When a schema requires login (post a tweet, add to cart, RSVP), you can use
the session you already have in Chrome, Firefox, Safari, Edge, or Brave —
no need to log in again:

```bash
hermai session import x.com
# Reads cookies scoped to x.com from your installed browsers,
# saves them to ~/.hermai/sessions/x.com/cookies.json
```

The first run surfaces an OS-level authorization prompt (macOS Keychain,
Windows DPAPI, Linux libsecret) — Hermai can't read your cookies without
your explicit consent at the OS level. Reads are always scoped to the
single domain you name; we never touch cookies for other sites.

Use `--dry-run` to see the cookie names without writing values to disk:

```bash
hermai session import x.com --dry-run
```

## Local cache

```bash
hermai catalog <url>                          # summarize cached endpoints for a URL
hermai schema <url>                           # show the cached schema JSON
hermai cache list                             # list cached domains
hermai init                                   # create ~/.hermai/config.yaml
hermai doctor                                 # verify your setup
```

`hermai --help` for the full command list.

## Docs

- Concepts + schema spec — [docs.hermai.ai](https://docs.hermai.ai)
- Hosted registry + dashboard — [hermai.ai](https://hermai.ai)

## License

[AGPL-3.0](LICENSE). Running a modified version as a hosted service requires publishing your changes.
