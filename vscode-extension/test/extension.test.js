'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const assert = require('node:assert/strict');

const {
  bundledBinaryPath,
  createServerDefinitions,
  repositoryGraphPath,
  workspaceGraphPath,
  runMcpTool
} = require('../extension');

test('bundledBinaryPath selects every packaged platform binary', () => {
  const cases = [
    ['darwin', 'arm64', 'codebase-graph-mcp'],
    ['darwin', 'x64', 'codebase-graph-mcp'],
    ['linux', 'arm64', 'codebase-graph-mcp'],
    ['linux', 'x64', 'codebase-graph-mcp'],
    ['win32', 'arm64', 'codebase-graph-mcp.exe'],
    ['win32', 'x64', 'codebase-graph-mcp.exe']
  ];
  for (const [platform, architecture, executable] of cases) {
    assert.equal(
      bundledBinaryPath('/extension', platform, architecture),
      path.join('/extension', 'bin', `${platform}-${architecture}`, executable)
    );
  }
  assert.throws(
    () => bundledBinaryPath('/extension', 'freebsd', 'x64'),
    /unsupported platform freebsd-x64/
  );
});

test('createServerDefinitions creates one workspace-scoped server per local root', () => {
  class McpStdioServerDefinition {
    constructor(label, command, args, env, version) {
      Object.assign(this, { label, command, args, env, version });
    }
  }
  const folders = [
    { name: 'api', uri: { scheme: 'file', fsPath: '/work/api' } },
    { name: 'worker', uri: { scheme: 'file', fsPath: '/work/worker' } },
    { name: 'remote', uri: { scheme: 'vscode-vfs', fsPath: '/work/remote' } }
  ];
  const vscode = {
    McpStdioServerDefinition,
    workspace: { workspaceFolders: folders }
  };

  const definitions = createServerDefinitions(vscode, '/extension/codebase-graph-mcp');

  assert.equal(definitions.length, 2);
  assert.deepEqual(definitions.map(definition => definition.args), [
    ['--workspace', '/work/api'],
    ['--workspace', '/work/worker']
  ]);
  assert.deepEqual(definitions.map(definition => definition.cwd), [folders[0].uri, folders[1].uri]);
});

test('independent MCP processes share repository and workspace graphs', async t => {
  const repository = fs.mkdtempSync(path.join(os.tmpdir(), 'codebase-graph-vscode-'));
  t.after(() => fs.rmSync(repository, { recursive: true, force: true }));
  fs.writeFileSync(path.join(repository, 'go.mod'), 'module example.com/shared\n\ngo 1.24\n');
  fs.writeFileSync(path.join(repository, 'main.go'), 'package main\n\nfunc main() { helper() }\nfunc helper() {}\n');
  const canonicalRepository = fs.realpathSync(repository);

  const extensionRoot = path.resolve(__dirname, '..');
  const binary = bundledBinaryPath(extensionRoot, 'darwin', 'arm64');
  const indexed = await runMcpTool(binary, canonicalRepository, 'index_workspace');
  const status = await runMcpTool(binary, canonicalRepository, 'discover_repositories');

  assert.equal(indexed.repositories[0].graph_path, repositoryGraphPath(canonicalRepository));
  assert.equal(indexed.workspace_graph_path, workspaceGraphPath(canonicalRepository));
  assert.equal(status.repositories[0].graph_path, indexed.repositories[0].graph_path);
  assert.equal(status.repositories[0].project.project_id, indexed.repositories[0].project.project_id);
  assert.equal(fs.existsSync(repositoryGraphPath(canonicalRepository)), true);
  assert.equal(fs.existsSync(workspaceGraphPath(canonicalRepository)), true);
});
