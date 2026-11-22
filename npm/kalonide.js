#!/usr/bin/env node
// Runs the native Kalonide binary for this machine. The binary comes from
// one of the @kalonedge/kalonide-<os>-<arch> packages, which npm installs
// as an optional dependency matching the current platform.
"use strict";

const { spawn } = require("child_process");
const fs = require("fs");
const path = require("path");

const target = `${process.platform}-${process.arch}`;
const supported = ["darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64"];

function fail(msg) {
  process.stderr.write(`kalonide: ${msg}\n`);
  process.exit(1);
}

function binaryPath() {
  if (process.env.KALONIDE_BINARY) return process.env.KALONIDE_BINARY;
  if (!supported.includes(target)) {
    fail(`${target} is not supported yet (supported: ${supported.join(", ")}).`);
  }
  const pkg = `@kalonedge/kalonide-${target}`;
  let bin;
  try {
    bin = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), "bin", "kalonide");
  } catch {
    fail(
      `the package ${pkg} is missing. It is installed automatically unless optional\n` +
        `dependencies are turned off; reinstall with:  npm install -g kalonide`
    );
  }
  try {
    fs.accessSync(bin, fs.constants.X_OK);
  } catch {
    try {
      fs.chmodSync(bin, 0o755);
    } catch {
      // Report the real problem when the spawn below fails.
    }
  }
  return bin;
}

const child = spawn(binaryPath(), process.argv.slice(2), { stdio: "inherit" });

// The terminal delivers Ctrl-C and friends to Kalonide directly; this
// process just waits so the shell does not get the terminal back early.
for (const sig of ["SIGINT", "SIGQUIT"]) {
  process.on(sig, () => {});
}
for (const sig of ["SIGTERM", "SIGHUP"]) {
  process.on(sig, () => child.kill(sig));
}

child.on("error", (err) => fail(`could not start ${err.path || "the binary"}: ${err.message}`));
child.on("exit", (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code === null ? 1 : code);
});
