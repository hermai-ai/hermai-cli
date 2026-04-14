# hermai-cli

[English](./README.md) · [繁體中文](./README.zh-Hant.md) · **简体中文**

> 从终端发现、贡献并调用结构化的网站 API。

`hermai` 是 [Hermai registry](https://hermai.ai) 的开源 CLI — 一个由社区共同维护、专为 AI agent 打造的网站 API schema 目录。你可以探测一个网站记录其端点,将 schema 推送到目录,或拉取已有的 schema 供你的 agent 调用。

```bash
go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest
```

Homebrew、npm 和预编译的可执行文件即将推出。

## Agent skills

在 Claude Code、Codex、Cursor 或其他 agent 里使用?安装 skills,让 agent 知道如何调用这个 CLI:

```bash
npx skills add hermai-ai/hermai-skills
```

- **`hermai`** — 调用 registry 并使用已注册的 schema。
- **`hermai-contribute`** — 使用发现工具组为目录新增网站。

Repo:[hermai-ai/hermai-skills](https://github.com/hermai-ai/hermai-skills)。

## Registry

```bash
hermai registry login                         # GitHub OAuth,保存 API key
hermai registry list                          # 浏览目录
hermai registry pull <site> --intent "..."    # 下载 schema
hermai registry push schema.json              # 贡献 schema
```

## Discovery toolkit

用于编写新 schema 的确定性子命令。不需要 LLM key — 每个命令都输出 JSON,可直接传给下一步处理。

```bash
hermai detect <url>                          # 平台 + anti-bot 分类
hermai wellknown <domain>                    # robots、sitemap、RSS、GraphQL
hermai probe --body <url> | hermai extract   # 13 种嵌入数据模式
hermai intercept <url>                       # 在浏览器中捕获 XHR
hermai introspect <graphql-url>              # GraphQL schema
hermai replay request.json                   # 重放已捕获的请求
hermai session bootstrap <site>              # 为 anti-bot 网站预热浏览器 session
```

## Local cache

```bash
hermai catalog <url>                          # 显示该 URL 对应的已缓存端点摘要
hermai schema <url>                           # 显示已缓存的 schema JSON
hermai cache list                             # 列出已缓存的域名
hermai init                                   # 创建 ~/.hermai/config.yaml
hermai doctor                                 # 检查环境设置
```

运行 `hermai --help` 查看完整命令列表。

## Docs

- 概念与 schema 规范 — [docs.hermai.ai](https://docs.hermai.ai)
- 托管 registry 与 dashboard — [hermai.ai](https://hermai.ai)

## License

[AGPL-3.0](LICENSE)。将修改后的版本作为托管服务运行时,须公开你的改动。
