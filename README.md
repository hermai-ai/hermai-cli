# hermai-cli

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
hermai session bootstrap <site>              # warm browser for anti-bot sites
```

`hermai --help` for everything, `hermai doctor` to verify your setup.

## Docs

- Concepts + schema spec — [docs.hermai.ai](https://docs.hermai.ai)
- Hosted registry + dashboard — [hermai.ai](https://hermai.ai)

## License

[AGPL-3.0](LICENSE). Running a modified version as a hosted service requires publishing your changes.
