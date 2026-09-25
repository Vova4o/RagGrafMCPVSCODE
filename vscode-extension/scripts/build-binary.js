'use strict';

const childProcess = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const extensionRoot = path.resolve(__dirname, '..');
const projectRoot = path.resolve(extensionRoot, '..');
const distributionDirectory = path.join(extensionRoot, 'dist');
fs.mkdirSync(distributionDirectory, { recursive: true });

const targets = [
  { platform: 'darwin', architecture: 'arm64', goos: 'darwin', goarch: 'arm64' },
  { platform: 'darwin', architecture: 'x64', goos: 'darwin', goarch: 'amd64' },
  { platform: 'linux', architecture: 'arm64', goos: 'linux', goarch: 'arm64' },
  { platform: 'linux', architecture: 'x64', goos: 'linux', goarch: 'amd64' },
  { platform: 'win32', architecture: 'arm64', goos: 'windows', goarch: 'arm64' },
  { platform: 'win32', architecture: 'x64', goos: 'windows', goarch: 'amd64' }
];

const selectedTargets = process.argv.includes('--all')
  ? targets
  : targets.filter(target => target.platform === process.platform && target.architecture === process.arch);

if (selectedTargets.length === 0) {
  throw new Error(`unsupported local build target ${process.platform}-${process.arch}`);
}

for (const target of selectedTargets) {
  const targetName = `${target.platform}-${target.architecture}`;
  const outputDirectory = path.join(extensionRoot, 'bin', targetName);
  const executable = target.platform === 'win32' ? 'codebase-graph-mcp.exe' : 'codebase-graph-mcp';
  const output = path.join(outputDirectory, executable);
  fs.mkdirSync(outputDirectory, { recursive: true });

  const result = childProcess.spawnSync('go', [
    'build',
    '-trimpath',
    '-tags',
    'grammar_subset grammar_subset_javascript grammar_subset_typescript grammar_subset_tsx grammar_subset_python grammar_subset_rust grammar_subset_java grammar_subset_kotlin grammar_subset_c_sharp',
    '-o',
    output,
    './cmd/codebase-graph-mcp'
  ], {
    cwd: projectRoot,
    encoding: 'utf8',
    stdio: 'inherit',
    env: {
      ...process.env,
      CGO_ENABLED: '0',
      GOOS: target.goos,
      GOARCH: target.goarch
    }
  });

  if (result.error) {
    throw new Error(`start Go build for ${targetName}: ${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`Go build for ${targetName} exited with code ${result.status}`);
  }
  if (target.platform !== 'win32') {
    fs.chmodSync(output, 0o755);
  }
}
