'use strict';

// Single source of truth for mapping the current Node runtime to an ox release
// asset name. This MUST mirror two upstream files EXACTLY — any drift here ships
// a wrapper that downloads a nonexistent asset:
//
//   scripts/install.sh          detect_platform():   uname -s -> darwin|linux|freebsd
//                                                    uname -m -> amd64|arm64
//                               archive_name:        ox_${version#v}_${os}_${arch}.tar.gz
//
//   .config/goreleaser.yml      archives.name_template:
//                                 "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
//                               ({{ .Version }} is the tag WITHOUT the leading 'v')
//
// Node's process.platform values (darwin/linux/freebsd) already match goreleaser's
// {{ .Os }}. Only the arch names differ: Node says x64/arm64, Go says amd64/arm64.

// process.platform -> GOOS. Windows is intentionally absent: no ox binary is built
// for it (install.sh tells Windows users to download manually), so npm's own `os`
// field rejects the install before postinstall even runs.
const PLATFORM_MAP = Object.freeze({
  darwin: 'darwin',
  linux: 'linux',
  freebsd: 'freebsd',
});

// process.arch -> GOARCH.
const ARCH_MAP = Object.freeze({
  x64: 'amd64',
  arm64: 'arm64',
});

// The exact GOOS_GOARCH matrix the `ox` binary is built for in .config/goreleaser.yml.
// freebsd/arm64 is deliberately NOT built, so the coarse os/cpu OR-lists in
// package.json cannot express it — this set is the precise gate the postinstall uses.
const SUPPORTED = Object.freeze(
  new Set(['darwin_amd64', 'darwin_arm64', 'linux_amd64', 'linux_arm64', 'freebsd_amd64'])
);

/**
 * Resolve a Node platform/arch pair to the ox release target.
 * @returns {{supported: boolean, platform: string, arch: string, os?: string, goarch?: string, key?: string}}
 */
function target(platform = process.platform, arch = process.arch) {
  const os = PLATFORM_MAP[platform];
  const goarch = ARCH_MAP[arch];
  if (!os || !goarch) {
    return { supported: false, platform, arch, os, goarch };
  }
  const key = `${os}_${goarch}`;
  return { supported: SUPPORTED.has(key), platform, arch, os, goarch, key };
}

/**
 * Build the release asset (tarball) filename for a version + target.
 * @param {string} version package version WITHOUT the leading 'v' (matches goreleaser {{ .Version }})
 */
function assetName(version, t = target()) {
  return `ox_${version}_${t.os}_${t.goarch}.tar.gz`;
}

module.exports = { target, assetName, SUPPORTED, PLATFORM_MAP, ARCH_MAP };
