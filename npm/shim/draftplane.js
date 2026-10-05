#!/usr/bin/env node
'use strict';

// The launcher. `draftplane` on a user's PATH is this file; the real program is
// a Go binary living in one of the @draftplane/draftplane-<platform>-<arch>
// packages,
// exactly one of which npm installs because each declares `os` and `cpu`.
//
// A shim exists because npm cannot choose a `bin` conditionally. The platform
// packages could each declare their own, but then two of them installed at once
// would fight over the name, and the command would come from a package the user
// never asked for.

const { spawnSync } = require('child_process');
const path = require('path');

// process.platform and process.arch are Node's vocabulary -- "darwin"/"linux"
// and "arm64"/"x64" -- and the release build names its output directories to
// match, so this is a substitution rather than a lookup table. The one place
// Go's vocabulary (amd64) is translated is scripts/build-release.sh.
const target = `${process.platform}-${process.arch}`;
// The product name repeats inside its own scope deliberately: @draftplane is
// the whole npm scope, so a bare @draftplane/${target} would spend a name a
// second binary product under the same scope would also want. See
// build-npm.sh's PKG.
const pkg = `@draftplane/draftplane-${target}`;

const SUPPORTED = ['darwin-arm64', 'darwin-x64', 'linux-arm64', 'linux-x64'];

function resolveBinary() {
  // Resolve the package's manifest and walk to the binary beside it, rather
  // than resolving the binary directly: package.json is the one path a package
  // always exposes, and a binary is not a module specifier Node will resolve.
  let manifest;
  try {
    manifest = require.resolve(`${pkg}/package.json`);
  } catch {
    return null;
  }
  return path.join(path.dirname(manifest), 'bin', 'draftplane');
}

function fail(lines) {
  console.error(lines.join('\n'));
  process.exit(1);
}

const binary = resolveBinary();

if (binary === null) {
  // Two very different causes, and the message must not guess between them:
  // an unsupported platform, or a supported one whose optional dependency did
  // not install. Saying which is possible; saying both is honest.
  if (!SUPPORTED.includes(target)) {
    fail([
      `draftplane does not ship a binary for ${target}.`,
      '',
      `Supported: ${SUPPORTED.join(', ')}`,
      '',
      'Windows is not supported. Under WSL, install the linux build instead.',
      'Questions: bugs@draftplane.io',
    ]);
  }
  fail([
    `draftplane could not find its binary for ${target}.`,
    '',
    `The package ${pkg} should have been installed alongside this one.`,
    'This usually means the install ran with optional dependencies disabled',
    '(--no-optional, or --omit=optional).',
    '',
    'Try:  npm install --force draftplane',
    'Still broken? bugs@draftplane.io',
  ]);
}

// stdio: 'inherit' hands the real terminal to the child, which a TUI requires:
// it needs a tty for raw mode and for its size. It also means Ctrl-C reaches
// the child directly, because the signal goes to the foreground process group
// rather than through this process.
const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });

if (result.error) {
  fail([
    `draftplane could not run its binary at ${binary}`,
    `${result.error.message}`,
    '',
    'If this says "permission denied", the package was likely extracted by a',
    'tool that dropped the executable bit.',
    '',
    'bugs@draftplane.io',
  ]);
}

// A child killed by a signal reports status null. Exiting 1 there would claim
// an ordinary failure; the shell convention for "died on signal N" is 128+N,
// and reproducing it keeps `draftplane; echo $?` meaning what it means for any
// other program.
if (result.status === null && result.signal) {
  const os = require('os');
  const signum = os.constants.signals[result.signal];
  process.exit(signum ? 128 + signum : 1);
}

process.exit(result.status === null ? 1 : result.status);
