#!/usr/bin/env node
// Thin exec wrapper — forwards argv and stdio to the platform binary that
// scripts/postinstall.js dropped next to us.
"use strict";

const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const binName = process.platform === "win32" ? "hermai.exe" : "hermai";
const binPath = path.join(__dirname, binName);

if (!fs.existsSync(binPath)) {
  console.error(
    `[hermai-cli] binary not found at ${binPath}.\n` +
      `This usually means postinstall was skipped. Reinstall with:\n` +
      `  npm install -g hermai-cli\n` +
      `Or run: node ${path.join(__dirname, "..", "scripts", "postinstall.js")}`
  );
  process.exit(1);
}

const r = spawnSync(binPath, process.argv.slice(2), { stdio: "inherit" });
if (r.error) {
  console.error(`[hermai-cli] ${r.error.message}`);
  process.exit(1);
}
process.exit(r.status ?? 0);
