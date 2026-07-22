#!/usr/bin/env node
/**
 * Postinstall: download matching nightgauge binary from GitHub Releases.
 *
 * - Downloads the correct binary for the current platform/arch
 * - Verifies SHA256 checksum against manifest.json
 * - Falls back to `go build` if Go is present and download fails
 * - Non-blocking: warns and exits 0 if all methods fail
 *
 * Environment variables:
 *   NIGHTGAUGE_SKIP_POSTINSTALL=1  — skip binary download entirely
 */

"use strict";

const https = require("https");
const http = require("http");
const fs = require("fs");
const nodeOs = require("os");
const path = require("path");
const crypto = require("crypto");
const { execSync } = require("child_process");
const { detectPlatform, getArchiveName } = require("./detect-platform");
const zlib = require("zlib");

const PKG = require("../package.json");
const VERSION = PKG.version;
const BIN_DIR = path.join(__dirname, "..", "dist", "bin");
const BINARY_NAME = process.platform === "win32" ? "nightgauge.exe" : "nightgauge";
const BINARY_PATH = path.join(BIN_DIR, BINARY_NAME);
const GITHUB_RELEASE_BASE = "https://github.com/nightgauge/nightgauge/releases/download";

function log(msg) {
  process.stderr.write(`[nightgauge-sdk postinstall] ${msg}\n`);
}

/**
 * Follow redirects and download a URL to a buffer.
 */
function download(url, maxRedirects = 5) {
  return new Promise((resolve, reject) => {
    if (maxRedirects <= 0) {
      return reject(new Error("Too many redirects"));
    }

    const client = url.startsWith("https") ? https : http;
    client
      .get(url, { headers: { "User-Agent": "nightgauge-sdk" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          return resolve(download(res.headers.location, maxRedirects - 1));
        }

        if (res.statusCode !== 200) {
          res.resume();
          return reject(new Error(`HTTP ${res.statusCode} fetching ${url}`));
        }

        const chunks = [];
        res.on("data", (chunk) => chunks.push(chunk));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

/**
 * Fetch manifest.json from the GitHub Release.
 */
async function fetchManifest(version) {
  const url = `${GITHUB_RELEASE_BASE}/v${version}/manifest.json`;
  log(`Fetching manifest: ${url}`);
  const buf = await download(url);
  return JSON.parse(buf.toString("utf8"));
}

/**
 * Verify SHA256 checksum of a file.
 */
function verifyChecksum(filePath, expected) {
  const hash = crypto.createHash("sha256");
  const data = fs.readFileSync(filePath);
  hash.update(data);
  const actual = `sha256:${hash.digest("hex")}`;
  if (actual !== expected) {
    throw new Error(`Checksum mismatch: expected ${expected}, got ${actual}`);
  }
  log("Checksum verified");
}

/**
 * Extract a tar.gz archive to get the binary.
 */
function extractBinary(archivePath, destDir) {
  fs.mkdirSync(destDir, { recursive: true });

  // Use zlib + manual tar extraction (no npm tar dependency needed)
  const data = fs.readFileSync(archivePath);
  const decompressed = zlib.gunzipSync(data);

  // Simple tar extraction: find the binary file in the archive
  // tar format: 512-byte header blocks followed by file data
  let offset = 0;
  while (offset < decompressed.length) {
    const header = decompressed.slice(offset, offset + 512);
    // Check for end-of-archive (two zero blocks)
    if (header.every((b) => b === 0)) break;

    const name = header.slice(0, 100).toString("utf8").replace(/\0/g, "").trim();
    const sizeOctal = header.slice(124, 136).toString("utf8").replace(/\0/g, "").trim();
    const fileSize = parseInt(sizeOctal, 8) || 0;
    const typeFlag = String.fromCharCode(header[156]);

    offset += 512; // move past header

    if (typeFlag === "0" || typeFlag === "\0") {
      // Regular file
      const basename = path.basename(name);
      if (basename === BINARY_NAME || basename === "nightgauge") {
        const fileData = decompressed.slice(offset, offset + fileSize);
        const destPath = path.join(destDir, BINARY_NAME);
        fs.writeFileSync(destPath, fileData);
        fs.chmodSync(destPath, 0o755);
        log(`Extracted: ${destPath}`);
      }
    }

    // Advance to next 512-byte boundary
    offset += Math.ceil(fileSize / 512) * 512;
  }
}

/**
 * Try to build from source using go build.
 */
function tryGoBuild() {
  try {
    execSync("go version", { stdio: "ignore" });
  } catch {
    return false;
  }

  log("Go found — building from source...");
  try {
    fs.mkdirSync(BIN_DIR, { recursive: true });
    execSync(`go build -o "${BINARY_PATH}" github.com/nightgauge/nightgauge/cmd/nightgauge`, {
      stdio: "inherit",
    });
    log(`Binary built from source: ${BINARY_PATH}`);
    return true;
  } catch (err) {
    log(`Source build failed: ${err.message}`);
    return false;
  }
}

async function main() {
  // Allow skipping postinstall entirely
  if (process.env.NIGHTGAUGE_SKIP_POSTINSTALL === "1") {
    log("Skipping postinstall (NIGHTGAUGE_SKIP_POSTINSTALL=1)");
    return;
  }

  // Check if binary already exists
  if (fs.existsSync(BINARY_PATH)) {
    log(`Binary already exists: ${BINARY_PATH}`);
    return;
  }

  const platform = detectPlatform();
  if (!platform) {
    log(`Unsupported platform: ${process.platform}/${process.arch} — skipping binary download`);
    return;
  }

  const { os, arch } = platform;
  const archiveName = getArchiveName(VERSION, os, arch);
  const archiveUrl = `${GITHUB_RELEASE_BASE}/v${VERSION}/${archiveName}`;

  try {
    // Fetch manifest for checksum verification
    const manifest = await fetchManifest(VERSION);
    const expectedChecksum = manifest.files[archiveName];
    if (!expectedChecksum) {
      throw new Error(`No manifest entry for ${archiveName}`);
    }

    // Download the archive
    log(`Downloading: ${archiveUrl}`);
    const archiveData = await download(archiveUrl);

    // Write to temp file for verification
    const tmpDir = fs.mkdtempSync(path.join(nodeOs.tmpdir(), "ib-"));
    const tmpArchive = path.join(tmpDir, archiveName);
    fs.writeFileSync(tmpArchive, archiveData);

    // Verify checksum
    verifyChecksum(tmpArchive, expectedChecksum);

    // Extract binary
    extractBinary(tmpArchive, BIN_DIR);

    // Cleanup temp files
    fs.rmSync(tmpDir, { recursive: true, force: true });

    if (fs.existsSync(BINARY_PATH)) {
      log(`Binary installed: ${BINARY_PATH}`);
    } else {
      throw new Error("Binary not found after extraction");
    }
  } catch (err) {
    log(`Download failed: ${err.message}`);

    // Fallback: try go build
    if (!tryGoBuild()) {
      log(
        "WARNING: Binary not installed. Install manually:\n" +
          "  brew install nightgauge/tap/nightgauge\n" +
          "  go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest"
      );
    }
  }
}

main().catch((err) => {
  // Non-blocking: postinstall must always exit 0
  log(`Unexpected error: ${err.message}`);
  process.exit(0);
});
