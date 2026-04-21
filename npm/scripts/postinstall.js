#!/usr/bin/env node
// Downloads the platform-specific hermai binary from the GitHub release
// that matches this package's version, verifies its sha256 against the
// release's checksums.txt, and lands it at bin/hermai(.exe) for the
// wrapper in bin/hermai.js to exec. Runs on `npm install`.
//
// Kept deliberately dependency-free: no npm deps means no transitive
// vulnerabilities at install time, and the script works on the older
// Node 16 engine floor declared in package.json.
"use strict";

const https = require("https");
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const zlib = require("zlib");
const { execFileSync } = require("child_process");

const pkg = require("../package.json");
const RELEASE_TAG = `v${pkg.version}`;
const BASE = `https://github.com/hermai-ai/hermai-cli/releases/download/${RELEASE_TAG}`;

// Map the Node platform/arch triple to the goreleaser archive name produced
// by .goreleaser.yaml — tarballs for unix, zip for windows.
function target() {
  const p = process.platform;
  const a = process.arch;
  const arch = a === "x64" ? "amd64" : a === "arm64" ? "arm64" : null;
  if (!arch) return null;
  if (p === "darwin") return { os: "darwin", arch, ext: "tar.gz" };
  if (p === "linux") return { os: "linux", arch, ext: "tar.gz" };
  if (p === "win32") return { os: "windows", arch, ext: "zip" };
  return null;
}

function die(msg) {
  console.error(`\n[hermai-cli] ${msg}`);
  console.error(
    "Install a prebuilt binary manually from https://github.com/hermai-ai/hermai-cli/releases"
  );
  console.error("or `go install github.com/hermai-ai/hermai-cli/cmd/hermai@latest`.\n");
  process.exit(1);
}

// npm sets npm_config_offline=true for `npm ci --offline` / restricted CI;
// skip the download and assume the binary will be provided some other way.
if (process.env.npm_config_offline === "true" || process.env.HERMAI_SKIP_POSTINSTALL === "1") {
  console.log("[hermai-cli] skipping binary download (offline / HERMAI_SKIP_POSTINSTALL)");
  process.exit(0);
}

const t = target();
if (!t) die(`unsupported platform ${process.platform}/${process.arch}`);

const archive = `hermai_${pkg.version}_${t.os}_${t.arch}.${t.ext}`;
const archiveUrl = `${BASE}/${archive}`;
const checksumsUrl = `${BASE}/checksums.txt`;
const binName = t.os === "windows" ? "hermai.exe" : "hermai";
const binDir = path.join(__dirname, "..", "bin");
const binPath = path.join(binDir, binName);

// Follow up to 5 redirects. GitHub release assets redirect through
// objects.githubusercontent.com with a signed URL.
function get(url, sink, redirects = 5) {
  return new Promise((resolve, reject) => {
    https
      .get(url, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          if (redirects <= 0) return reject(new Error(`too many redirects for ${url}`));
          res.resume();
          return resolve(get(res.headers.location, sink, redirects - 1));
        }
        if (res.statusCode !== 200) {
          res.resume();
          return reject(new Error(`GET ${url} -> HTTP ${res.statusCode}`));
        }
        sink(res, resolve, reject);
      })
      .on("error", reject);
  });
}

function download(url, dest) {
  return get(url, (res, resolve, reject) => {
    const file = fs.createWriteStream(dest);
    res.pipe(file);
    file.on("finish", () => file.close(() => resolve(dest)));
    file.on("error", reject);
  });
}

function readUrl(url) {
  return get(url, (res, resolve, reject) => {
    const chunks = [];
    res.on("data", (c) => chunks.push(c));
    res.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    res.on("error", reject);
  });
}

function sha256File(p) {
  const h = crypto.createHash("sha256");
  h.update(fs.readFileSync(p));
  return h.digest("hex");
}

function extractTarGz(archivePath, outDir) {
  // Use the system tar — available on macOS, Linux, and Windows 10+.
  // Keeps the package dependency-free (no bundled tar library).
  execFileSync("tar", ["-xzf", archivePath, "-C", outDir], { stdio: "inherit" });
}

function extractZip(archivePath, outDir) {
  // Stream the zip central directory manually. Goreleaser's default zip
  // is flat (no nested dirs), so we only need to handle the single entry
  // for hermai.exe (+ LICENSE, README which we skip). Uses zlib.inflateRaw
  // to decompress DEFLATE entries — the only compression method
  // goreleaser writes.
  const buf = fs.readFileSync(archivePath);
  // Find End of Central Directory record (signature 0x06054b50).
  let eocd = -1;
  for (let i = buf.length - 22; i >= 0 && i >= buf.length - 22 - 65536; i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new Error("zip EOCD not found");
  const cdOffset = buf.readUInt32LE(eocd + 16);
  const cdEntries = buf.readUInt16LE(eocd + 10);
  let p = cdOffset;
  for (let i = 0; i < cdEntries; i++) {
    if (buf.readUInt32LE(p) !== 0x02014b50) throw new Error("bad central directory entry");
    const compressionMethod = buf.readUInt16LE(p + 10);
    const compSize = buf.readUInt32LE(p + 20);
    const nameLen = buf.readUInt16LE(p + 28);
    const extraLen = buf.readUInt16LE(p + 30);
    const commentLen = buf.readUInt16LE(p + 32);
    const localOffset = buf.readUInt32LE(p + 42);
    const name = buf.slice(p + 46, p + 46 + nameLen).toString("utf8");
    p += 46 + nameLen + extraLen + commentLen;

    if (!name.endsWith(".exe")) continue; // only extract the binary

    // Local file header to find the actual data offset.
    if (buf.readUInt32LE(localOffset) !== 0x04034b50) throw new Error("bad local header");
    const lfNameLen = buf.readUInt16LE(localOffset + 26);
    const lfExtraLen = buf.readUInt16LE(localOffset + 28);
    const dataOffset = localOffset + 30 + lfNameLen + lfExtraLen;
    const data = buf.slice(dataOffset, dataOffset + compSize);

    const content = compressionMethod === 0 ? data : zlib.inflateRawSync(data);
    fs.writeFileSync(path.join(outDir, path.basename(name)), content);
  }
}

(async () => {
  try {
    fs.mkdirSync(binDir, { recursive: true });

    const tmpArchive = path.join(binDir, archive);
    console.log(`[hermai-cli] downloading ${archiveUrl}`);
    await download(archiveUrl, tmpArchive);

    const checksums = await readUrl(checksumsUrl);
    const want = checksums
      .split(/\r?\n/)
      .map((l) => l.trim().split(/\s+/))
      .find((cols) => cols[1] === archive);
    if (!want) throw new Error(`${archive} not found in checksums.txt`);

    const got = sha256File(tmpArchive);
    if (got !== want[0]) {
      throw new Error(`checksum mismatch for ${archive}: got ${got}, want ${want[0]}`);
    }

    if (t.ext === "tar.gz") extractTarGz(tmpArchive, binDir);
    else extractZip(tmpArchive, binDir);

    if (!fs.existsSync(binPath)) throw new Error(`binary ${binName} not found after extract`);
    if (t.os !== "windows") fs.chmodSync(binPath, 0o755);
    fs.unlinkSync(tmpArchive);

    console.log(`[hermai-cli] installed ${binPath}`);
  } catch (err) {
    die(`install failed: ${err.message}`);
  }
})();
