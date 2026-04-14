# hermai-cli

> Turn any website into structured JSON for AI agents.

`hermai` captures the same XHRs a page's own JavaScript uses, caches them as a schema, and replays that schema on every future call as a single HTTP request. No browser, no scraper, no LLM key required for the cached path.

```bash
go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest
```

Homebrew, npm, and prebuilt binaries coming soon.

## Agent skills

Running in Claude Code, Codex, Cursor, or another agent? Install the skills so the agent knows how to use this CLI:

```bash
npx skills add hermai-ai/hermai-skills
```

- **`hermai`** — call the registry, fetch data from already-registered sites.
- **`hermai-contribute`** — use the discovery toolkit to add a new site.

Repo: [hermai-ai/hermai-skills](https://github.com/hermai-ai/hermai-skills).

## Use

```bash
hermai fetch https://example.com/products/abc   # structured JSON from a page
hermai catalog https://example.com              # list discovered endpoints
hermai execute https://example.com/search '{"q":"laptop"}'
```

`hermai --help` for everything, `hermai doctor` to verify your environment.

## Discovery toolkit

Deterministic subcommands for documenting a site and pushing a schema to [hermai.ai](https://hermai.ai). No LLM key; each command prints JSON the next step can consume.

```bash
hermai detect <url>                          # platform + anti-bot classification
hermai wellknown <domain>                    # robots, sitemap, RSS, GraphQL
hermai probe --body <url> | hermai extract   # 13 embedded-data patterns
hermai intercept <url>                       # capture XHR in a browser
hermai introspect <graphql-url>              # GraphQL schema
hermai replay request.json                   # replay a captured request
hermai session bootstrap <site>              # warm browser for anti-bot sites
hermai registry push schema.json             # contribute to the catalog
```

## Docs

- Concepts + schema spec — [docs.hermai.ai](https://docs.hermai.ai)
- Hosted registry + dashboard — [hermai.ai](https://hermai.ai)

## License

[AGPL-3.0](LICENSE). Running a modified version as a hosted service requires publishing your changes.
