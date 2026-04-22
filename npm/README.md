# hermai-cli

npm wrapper for the [hermai](https://hermai.ai) CLI — turns any website into a structured JSON API for AI agents.

## Install

```bash
npm install -g hermai-cli
# or
npx hermai-cli --help
```

On install, the package's `postinstall` downloads the correct prebuilt
binary for your platform (macOS/Linux/Windows × amd64/arm64) from the
corresponding GitHub release, verifies its sha256 against the release's
`checksums.txt`, and places it alongside the wrapper. No `cgo`, no Go
toolchain required.

## Environment

- `HERMAI_SKIP_POSTINSTALL=1` skips the binary download at install time.
  Useful in sandboxed CI where outbound network to GitHub releases is
  blocked; you'll need to drop the binary at `node_modules/hermai-cli/bin/hermai`
  yourself.

## Other install channels

- `brew install hermai-ai/hermai/hermai` (Homebrew, macOS/Linux)
- `go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest` (Go toolchain)
- GitHub Releases tarballs — https://github.com/hermai-ai/hermai-cli/releases

## Usage

See [docs.hermai.ai](https://docs.hermai.ai).

## License

AGPL-3.0-or-later — same as the upstream binary.
