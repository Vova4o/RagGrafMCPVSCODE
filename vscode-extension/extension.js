'use strict';

const childProcess = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const providerID = 'codebaseGraph.repositories';
const serverVersion = '0.2.4';
const protocolVersion = '2025-06-18';

function activate(context) {
  const vscode = require('vscode');
  const output = vscode.window.createOutputChannel('Codebase Graph');
  const definitionsChanged = new vscode.EventEmitter();
  const binary = bundledBinaryPath(context.extensionPath);

  context.subscriptions.push(output, definitionsChanged);
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => definitionsChanged.fire()));
  context.subscriptions.push(vscode.lm.registerMcpServerDefinitionProvider(providerID, {
    onDidChangeMcpServerDefinitions: definitionsChanged.event,
    provideMcpServerDefinitions: () => createServerDefinitions(vscode, binary),
    resolveMcpServerDefinition: async server => {
      await ensureExecutable(binary);
      return server;
    }
  }));

  context.subscriptions.push(vscode.commands.registerCommand('codebaseGraph.indexRepository', async () => {
    const folder = await pickWorkspaceFolder(vscode);
    if (!folder) {
      return;
    }
    await vscode.window.withProgress({
      location: vscode.ProgressLocation.Notification,
      title: `Indexing ${folder.name}`,
      cancellable: true
    }, async (_progress, token) => {
      try {
        await ensureExecutable(binary);
        const result = await runMcpTool(binary, folder.uri.fsPath, 'index_workspace', {}, token);
        output.appendLine(JSON.stringify(result, null, 2));
        vscode.window.showInformationMessage(
          `Codebase Graph: indexed ${result.repositories.length} repositories, ${result.workspace.nodes} nodes, ${result.workspace.edges} edges.`
        );
      } catch (error) {
        reportError(vscode, output, 'index repository', error);
      }
    });
  }));

  context.subscriptions.push(vscode.commands.registerCommand('codebaseGraph.showStatus', async () => {
    const folder = await pickWorkspaceFolder(vscode);
    if (!folder) {
      return;
    }
    try {
      await ensureExecutable(binary);
      const result = await runMcpTool(binary, folder.uri.fsPath, 'discover_repositories');
      output.appendLine(JSON.stringify(result, null, 2));
      output.show(true);
      const indexed = result.repositories.filter(repository => repository.indexed);
      if (indexed.length === 0) {
        vscode.window.showInformationMessage(`Codebase Graph: ${folder.name} has no indexed repositories.`);
        return;
      }
      vscode.window.showInformationMessage(
        `Codebase Graph: ${indexed.length}/${result.repositories.length} repositories indexed; aggregate ${result.workspace_graph_path}.`
      );
    } catch (error) {
      reportError(vscode, output, 'read index status', error);
    }
  }));

  context.subscriptions.push(vscode.commands.registerCommand('codebaseGraph.openGraph', async () => {
    const folder = await pickWorkspaceFolder(vscode);
    if (!folder) {
      return;
    }
    const graph = workspaceGraphPath(folder.uri.fsPath);
    if (!fs.existsSync(graph)) {
      vscode.window.showInformationMessage(`Codebase Graph: ${folder.name} has not been indexed.`);
      return;
    }
    const document = await vscode.workspace.openTextDocument(vscode.Uri.file(graph));
    await vscode.window.showTextDocument(document, { preview: true });
  }));
}

function createServerDefinitions(vscode, binary) {
  return (vscode.workspace.workspaceFolders || [])
    .filter(folder => folder.uri.scheme === 'file')
    .map(folder => {
      const definition = new vscode.McpStdioServerDefinition(
        `Codebase Graph (${folder.name})`,
        binary,
        ['--workspace', folder.uri.fsPath],
        {},
        serverVersion
      );
      definition.cwd = folder.uri;
      return definition;
    });
}

async function pickWorkspaceFolder(vscode) {
  const folders = (vscode.workspace.workspaceFolders || []).filter(folder => folder.uri.scheme === 'file');
  if (folders.length === 0) {
    vscode.window.showErrorMessage('Codebase Graph requires an open local workspace folder.');
    return undefined;
  }
  if (folders.length === 1) {
    return folders[0];
  }
  const picked = await vscode.window.showQuickPick(
    folders.map(folder => ({ label: folder.name, description: folder.uri.fsPath, folder })),
    { placeHolder: 'Select the repository to use' }
  );
  return picked && picked.folder;
}

function bundledBinaryPath(extensionPath, platform = process.platform, architecture = process.arch) {
  const supported = new Set([
    'darwin-arm64'
  ]);
  const target = `${platform}-${architecture}`;
  if (!supported.has(target)) {
    throw new Error(`unsupported platform ${target}; this local VSIX contains only the darwin-arm64 server`);
  }
  const executable = platform === 'win32' ? 'codebase-graph-mcp.exe' : 'codebase-graph-mcp';
  return path.join(extensionPath, 'bin', target, executable);
}

async function ensureExecutable(binary) {
  let info;
  try {
    info = await fs.promises.stat(binary);
  } catch (error) {
    throw new Error(`bundled MCP server is unavailable at ${binary}: ${error.message}`);
  }
  if (!info.isFile()) {
    throw new Error(`bundled MCP server path is not a file: ${binary}`);
  }
  if (process.platform !== 'win32') {
    await fs.promises.chmod(binary, 0o755);
  }
}

function repositoryGraphPath(repository) {
  return path.join(repository, '.codebase-graph', 'graph.json');
}

function workspaceGraphPath(workspace) {
  return path.join(workspace, '.codebase-graph', 'workspace.json');
}

function runMcpTool(binary, workspace, tool, args = {}, cancellationToken) {
  return new Promise((resolve, reject) => {
    const child = childProcess.spawn(binary, ['--workspace', workspace], {
      cwd: workspace,
      stdio: ['pipe', 'pipe', 'pipe']
    });
    let stdout = '';
    let stderr = '';
    let settled = false;
    let cancelled = false;

    const finish = (callback, value) => {
      if (settled) {
        return;
      }
      settled = true;
      clearTimeout(timeout);
      cancellationDisposable?.dispose();
      callback(value);
    };

    const timeout = setTimeout(() => {
      child.kill();
      finish(reject, new Error(`MCP tool ${tool} timed out after 120 seconds`));
    }, 120000);
    const cancellationDisposable = cancellationToken?.onCancellationRequested(() => {
      cancelled = true;
      child.kill();
    });

    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', chunk => { stdout += chunk; });
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.on('error', error => finish(reject, new Error(`start MCP server: ${error.message}`)));
    child.on('close', code => {
      if (cancelled) {
        finish(reject, new Error(`MCP tool ${tool} was cancelled`));
        return;
      }
      if (code !== 0) {
        finish(reject, new Error(`MCP server exited with code ${code}: ${stderr.trim()}`));
        return;
      }
      try {
        const responses = stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
        const response = responses.find(candidate => candidate.id === 2);
        if (!response) {
          throw new Error('MCP server returned no tool response');
        }
        if (response.error) {
          throw new Error(response.error.message || JSON.stringify(response.error));
        }
        if (response.result?.isError) {
          throw new Error(response.result.content?.[0]?.text || `MCP tool ${tool} failed`);
        }
        const text = response.result?.content?.find(item => item.type === 'text')?.text;
        if (!text) {
          throw new Error(`MCP tool ${tool} returned no text content`);
        }
        finish(resolve, JSON.parse(text));
      } catch (error) {
        finish(reject, new Error(`decode MCP tool ${tool} response: ${error.message}`));
      }
    });

    child.stdin.end([
      JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: {
          protocolVersion,
          capabilities: {},
          clientInfo: { name: 'codebase-graph-vscode', version: serverVersion }
        }
      }),
      JSON.stringify({
        jsonrpc: '2.0',
        id: 2,
        method: 'tools/call',
        params: { name: tool, arguments: args }
      })
    ].join('\n') + '\n');
  });
}

function reportError(vscode, output, operation, error) {
  const message = error instanceof Error ? error.message : String(error);
  output.appendLine(`${operation}: ${message}`);
  output.show(true);
  vscode.window.showErrorMessage(`Codebase Graph: ${message}`);
}

function deactivate() {}

module.exports = {
  activate,
  deactivate,
  bundledBinaryPath,
  createServerDefinitions,
  ensureExecutable,
  repositoryGraphPath,
  workspaceGraphPath,
  runMcpTool
};
