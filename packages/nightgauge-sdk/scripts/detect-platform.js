#!/usr/bin/env node
/**
 * Platform/architecture detection helper for nightgauge binary download.
 * Maps Node.js process.platform/arch to goreleaser naming conventions.
 *
 * @returns {{ os: string, arch: string } | null}
 */

"use strict";

const PLATFORM_MAP = {
  darwin: "darwin",
  linux: "linux",
};

const ARCH_MAP = {
  arm64: "arm64",
  x64: "amd64",
};

/**
 * Detect the current platform and architecture.
 * Returns null if the platform is unsupported.
 */
function detectPlatform() {
  const os = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];

  if (!os || !arch) {
    return null;
  }

  return { os, arch };
}

/**
 * Build the archive filename for a given version and platform.
 */
function getArchiveName(version, os, arch) {
  return `nightgauge_${version}_${os}_${arch}.tar.gz`;
}

module.exports = { detectPlatform, getArchiveName, PLATFORM_MAP, ARCH_MAP };
