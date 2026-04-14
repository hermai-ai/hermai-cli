# hermai-cli

> The open-source engine that turns any website into structured JSON for AI agents.

`hermai` discovers website APIs by watching browser network traffic, caches what it learns as a schema, and replays the schema on future runs without launching a browser. It is the local execution engine behind the [Hermai](https://hermai.ai) hosted platform.

```
URL  ─►  probe  ─►  schema  ─►  fast JSON forever
                       │
                       └─►  pushed to hermai.ai (optional)
```

## Why

Agents that need data from websites without public APIs end up in one of two bad places: they ship a brittle CSS scraper, or they pay a hosted browser per page-load. `hermai` does the discovery work *once* — capturing the same XHRs the page itself uses — then re-uses that knowledge as a cached schema. The first call is slow. Every call after is a single HTTP request.

## Install

Until binaries are published:

```bash
go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest
```

Brew, npm, and pre-built release binaries are coming soon — see [issues](https://github.com/hermai-ai/hermai-cli/issues) for status.

## Quickstart

```bash
# Discover the API behind a page and emit structured JSON
hermai fetch https://example.com/products/abc

# Show every action and endpoint discovered for a site
hermai catalog https://example.com

# Execute an action without a browser (replays a cached schema)
hermai execute https://example.com/search '{"q":"laptop"}'

# Inspect or clear the local schema cache
hermai schema https://example.com
hermai cache list
```

Run `hermai --help` for the full command list, and `hermai doctor` to verify your environment is ready.

## Discovery toolkit

A set of deterministic subcommands for inspecting a site and contributing a schema to the registry. No LLM key required — each tool does one thing and emits structured output the next step can consume.

```bash
hermai detect <url>                          # classify platform + anti-bot
hermai wellknown <domain>                    # probe robots, sitemap, RSS, GraphQL
hermai probe --body <url> | hermai extract   # pull embedded data patterns
hermai intercept <url>                       # capture XHR calls in a browser
hermai introspect <graphql-url>              # GraphQL schema via introspection
hermai replay request.json                   # replay captured requests
hermai session bootstrap <site>              # warm a browser session for anti-bot sites
hermai registry push schema.json             # contribute to the hermai.ai catalog
```

`hermai extract` recognises 13 embedded-data patterns — `__NEXT_DATA__`, `ytInitialData`, `__APOLLO_STATE__`, `__NUXT_DATA__`, `SIGI_STATE`, `__PRELOADED_STATE__`, and more. Run `hermai extract --list-patterns` for the full list.

## Agent skills

If you're using Claude Code, Codex, Cursor, or another agent with the [Vercel skills CLI](https://github.com/vercel-labs/skills), install the Hermai skills so your agent knows how to use this CLI:

```bash
npx skills add hermai-ai/hermai-skills
```

Two skills ship together:

- **`hermai`** — teaches agents to call the registry and fetch data from already-registered sites.
- **`hermai-contribute`** — teaches agents the discovery toolkit and schema format so they can add new sites to the catalog.

Details: [hermai-ai/hermai-skills](https://github.com/hermai-ai/hermai-skills).

## How it works

`hermai` walks a cheapest-path-first pipeline:

1. **Probe** — try the obvious things first: `__NEXT_DATA__`, JSON-LD, sitemap, robots, common API conventions. ~83% of pages resolve here in <2 seconds with no browser.
2. **HTML extract** — selector-driven extraction for pages that ship their data inline.
3. **Browser + analyzer** — only as a last resort: launch a stealth Chromium via [go-rod](https://github.com/go-rod/rod), capture every XHR, and let an LLM generalize the discovered endpoints into a reusable schema.

The output of step 3 is a **schema** — a small YAML file describing how to talk to the site. Future calls skip steps 1–3 entirely and replay the schema as a single HTTP request.

## Project layout

```
cmd/hermai/   CLI entrypoint (cobra commands)
pkg/          Reusable engine — probe, browser, analyzer, fetcher, schema, actions, htmlext, cache
internal/     Private infrastructure: HTTP client, config, version
```

`pkg/` is intended to be importable as a Go module — the [hermai-api](https://github.com/hermai-ai/hermai-api) hosted platform consumes it directly.

## Hermai CLI vs Hermai hosted

`hermai-cli` is the engine. The [hermai.ai](https://hermai.ai) hosted platform builds on top of it: schemas you discover locally can be pushed to a community catalog, and the platform handles things that don't belong on your laptop — proxy rotation, anti-bot session management, cron-driven schema validation, and a credit-billed proxy endpoint that other agents can call.

You don't need an account to use the CLI. The hosted platform is opt-in.

## Documentation

Concepts, schema format, and API reference live at [docs.hermai.ai](https://docs.hermai.ai).

## Contributing

Issues and pull requests welcome. A `CONTRIBUTING.md` with the development setup, schema spec, and example schemas is on the way.

## License

[GNU Affero General Public License v3.0](LICENSE) — same as [Firecrawl](https://github.com/mendableai/firecrawl). If you run a modified version of `hermai` as a hosted service, AGPL requires you to make your changes available to your users.
