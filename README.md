# hermai-cli

**English** · [繁體中文](./README.zh-Hant.md) · [简体中文](./README.zh-Hans.md)

> Discover, contribute, and call structured website APIs from your terminal.

`hermai` is the open-source CLI for the [Hermai registry](https://hermai.ai) — a community catalog of website API schemas for AI agents. Probe a site to document its endpoints, push the schema to the catalog, or pull an existing schema and call it — including authenticated writes with per-request signing.

```bash
go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest
```

Homebrew, npm, and prebuilt binaries coming soon.

## Agent skill

Running in Claude Code, Codex, Cursor, or another agent? Install the Hermai skill so the agent knows how to use this CLI:

```bash
npx skills add hermai-ai/hermai-skills --skill hermai
```

One skill covers both audiences. When a user asks for data from a site, the agent pulls the schema and calls it. When a user wants to add a new site, the skill's contributor references (loaded on demand via progressive disclosure) walk the agent through discovery, schema authoring, and push.

Migrating from 1.x? The old `hermai-contribute` skill merged into `hermai` in 2.0. Run `npx skills update hermai` and `npx skills remove hermai-contribute`.

Repo: [hermai-ai/hermai-skills](https://github.com/hermai-ai/hermai-skills).

## Call a site as an API

```bash
# Pull a schema (API key required, GitHub sign-in at hermai.ai/dashboard)
hermai registry login
hermai registry pull x.com --intent "drafting a post from my agent"

# Read endpoints: call them directly with curl / fetch / any HTTP client.
# Authenticated writes: `hermai action` handles sessions + signing for you.
hermai action x.com CreateDraftTweet --arg text="drafted by hermai"
```

`hermai action` loads the schema, resolves the user's session (cookies from disk, or pulled from an installed browser on first run), runs any schema-declared bootstrap JS to compute per-session state, runs any per-request signer JS, fires via a Chrome-TLS fingerprinted HTTP client, and rotates `Set-Cookie` back on 2xx responses. Works against sites that require per-request signing (X's `x-client-transaction-id`, TikTok's `X-Bogus`) without opening a browser.

Useful flags:
- `--dry-run` — print the fully-signed request, don't hit the network
- `--schema <file>` — use a local schema JSON instead of the registry cache
- `--arg key=value` — repeatable, fills `{{var}}` placeholders in the schema's URL/body templates

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
hermai probe --body <url> | hermai extract   # 13 named embedded-data patterns plus
                                             # any <script type="application/json" id="X">
hermai intercept <url>                       # capture XHR in a browser
hermai introspect <graphql-url>              # GraphQL schema
hermai replay request.json                   # replay a captured request
hermai session bootstrap <site>              # warm a fresh browser session
hermai session import <site>                 # import cookies from your current browser
```

### Capturing authenticated write endpoints

Most interesting APIs are gated behind cookies. To capture a write flow (add-to-cart, save-draft, submit-review) without logging in a second time, combine headful intercept with session injection:

```bash
# 1. Pull cookies from the browser you're already signed in with
hermai session import example.com

# 2. Open a visible Chrome, pre-loaded with those cookies, and capture what you click
hermai intercept https://example.com/product/123 \
  --headful --session example.com \
  --timeout 120s --wait 100s --ndjson > capture.ndjson

# 3. Grep/jq the captured JSON for the XHR of interest
grep -F 'CreateOrder' capture.ndjson | jq '.request.body'
```

The request body goes straight into your schema's `body_template` with `{{var}}` placeholders for user-varying fields. **Capture, don't guess** — inventing body fields is the #1 cause of rejected write schemas.

`hermai introspect` takes `--header name=value` (repeatable) for auth-gated GraphQL endpoints like Shopify Storefront or Estée Lauder's Stardust.

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
- Skill reference: `hermai-ai/hermai-skills` — architecture, runtime, CLI, contributing

## License

[AGPL-3.0](LICENSE). Running a modified version as a hosted service requires publishing your changes.
