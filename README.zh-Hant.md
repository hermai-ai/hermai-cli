# hermai-cli

[English](./README.md) · **繁體中文** · [简体中文](./README.zh-Hans.md)

> 從終端探索、貢獻並呼叫結構化的網站 API。

`hermai` 是 [Hermai registry](https://hermai.ai) 的開源 CLI — 一個由社群共同維護、專為 AI agent 打造的網站 API schema 目錄。你可以探測一個網站記錄其端點,將 schema 推送到目錄,或拉取已有的 schema 讓你的 agent 呼叫。

```bash
go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest
```

Homebrew、npm 以及預編譯執行檔即將推出。

## Agent skills

在 Claude Code、Codex、Cursor 或其他 agent 裡使用?安裝 skills,讓 agent 知道如何呼叫這個 CLI:

```bash
npx skills add hermai-ai/hermai-skills
```

- **`hermai`** — 呼叫 registry 並使用既有的 schema。
- **`hermai-contribute`** — 使用探索工具組為目錄新增網站。

Repo:[hermai-ai/hermai-skills](https://github.com/hermai-ai/hermai-skills)。

## Registry

```bash
hermai registry login                         # GitHub OAuth,儲存 API key
hermai registry list                          # 瀏覽目錄
hermai registry pull <site> --intent "..."    # 下載 schema
hermai registry push schema.json              # 貢獻 schema
```

## Discovery toolkit

撰寫新 schema 用的確定性子指令。不需要 LLM key — 每個指令都輸出 JSON,可直接交給下一個步驟處理。

```bash
hermai detect <url>                          # 平台與 anti-bot 分類
hermai wellknown <domain>                    # robots、sitemap、RSS、GraphQL
hermai probe --body <url> | hermai extract   # 13 種內嵌資料模式
hermai intercept <url>                       # 在瀏覽器中捕獲 XHR
hermai introspect <graphql-url>              # GraphQL schema
hermai replay request.json                   # 重播已捕獲的請求
hermai session bootstrap <site>              # 為 anti-bot 網站預熱瀏覽器 session
```

## Local cache

```bash
hermai catalog <url>                          # 顯示該 URL 對應的已快取端點摘要
hermai schema <url>                           # 顯示已快取的 schema JSON
hermai cache list                             # 列出已快取的網域
hermai init                                   # 建立 ~/.hermai/config.yaml
hermai doctor                                 # 檢查環境設定
```

執行 `hermai --help` 查看完整指令列表。

## Docs

- 概念與 schema 規格 — [docs.hermai.ai](https://docs.hermai.ai)
- 託管 registry 與 dashboard — [hermai.ai](https://hermai.ai)

## License

[AGPL-3.0](LICENSE)。若將修改過的版本作為託管服務運行,須公開你的變更。
