'use strict';

const childProcess = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const extensionRoot = path.resolve(__dirname, '..');
const projectRoot = path.resolve(extensionRoot, '..');
const target = `${process.platform}-${process.arch}`;

if (target !== 'darwin-arm64') {
  throw new Error(`unsupported local build target ${target}`);
}

const outputDirectory = path.join(extensionRoot, 'bin', target);
const distributionDirectory = path.join(extensionRoot, 'dist');
fs.mkdirSync(outputDirectory, { recursive: true });
fs.mkdirSync(distributionDirectory, { recursive: true });

const output = path.join(outputDirectory, 'codebase-graph-mcp');
const result = childProcess.spawnSync('go', [
  'build',
  '-trimpath',
  '-o',
  output,
  './cmd/codebase-graph-mcp'
], {
  cwd: projectRoot,
  encoding: 'utf8',
  stdio: 'inherit'
});

if (result.error) {
  throw new Error(`start Go build: ${result.error.message}`);
}
if (result.status !== 0) {
  throw new Error(`Go build exited with code ${result.status}`);
}
fs.chmodSync(output, 0o755);
