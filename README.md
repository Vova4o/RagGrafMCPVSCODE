# Codebase Graph

`codebase-graph` is a local multi-language code graph shared by Codex, Claude Code,
Google Antigravity, and VS Code. It has no daemon and no per-agent databases.

## Graph layout

When a Git repository is opened directly, the plugin creates:

```text
<repo>/.codebase-graph/graph.json
<repo>/.codebase-graph/workspace.json
```

When an umbrella folder contains several Git repositories, it creates one graph in
each repository plus one aggregate graph at the workspace root:

```text
<workspace>/service-a/.codebase-graph/graph.json
<workspace>/service-b/.codebase-graph/graph.json
<workspace>/.codebase-graph/workspace.json
```

The aggregate graph contains the child graphs and repository-level `DEPENDS_ON`
edges inferred from module imports and explicit service URLs. Repository graphs are never merged on disk or
duplicated per client.

## Languages

The indexer recognizes Go, TypeScript, JavaScript, Python, SQL, shell, Rust, Java,
Kotlin, Ruby, PHP, C#, C, C++, Protocol Buffers, HTML, CSS, SCSS, Vue, and Svelte.
Go uses the standard library AST parser. Other languages use deterministic structural
parsers for files, declarations, imports, dependencies, and direct calls.

## Build and test

```bash
make build
go test -race ./...
go vet ./...
```

## Use without VS Code

VS Code is required only for the optional `.vsix` wrapper. The graph engine is a
standalone stdio MCP server and can be used directly by Codex, Claude Code,
Antigravity, or any other MCP client.

Install the local personal plugin in Codex:

```bash
codex plugin add codebase-graph@personal
```

Configure Claude Code and Antigravity for a workspace:

```bash
/absolute/path/to/codebase-graph/bin/codebase-graph-setup \
  --workspace /absolute/path/to/workspace
```

For another MCP client, add the server manually and always pin the actual
workspace with an absolute path:

```json
{
  "mcpServers": {
    "codebase-graph": {
      "command": "/absolute/path/to/codebase-graph/bin/codebase-graph-mcp",
      "args": ["--workspace", "/absolute/path/to/workspace"]
    }
  }
}
```

Start a new client session after installation so it loads the new MCP server and
graph-first instructions.

## Configure Claude Code and Antigravity

```bash
./bin/codebase-graph-setup --workspace /absolute/path/to/workspace
```

This merges a workspace-scoped MCP entry into `.mcp.json` and
`.agents/mcp_config.json`, installs the same graph skill for both clients, and
installs always-loaded graph-first rules in `.claude/rules/codebase-graph.md` and
`.agents/rules/codebase-graph.md`. Claude Code and Antigravity therefore receive
the workflow in every new project session instead of relying on conditional skill activation.
Existing unrelated definitions are preserved.
The installer first performs a real MCP `initialize` and `tools/list` handshake;
it refuses to write configuration unless the graph-first tools are advertised.
Its result contains `restart_required: true`: existing client sessions keep their
old MCP process and must be replaced with a new session after an update.

Verify the packaged server without changing client configuration:

```bash
./bin/codebase-graph-setup --action verify
```

To remove only these integrations:

```bash
./bin/codebase-graph-setup --action uninstall --workspace /absolute/path/to/workspace
```

## VS Code

```bash
make vscode-install
```

The extension registers one MCP server per open workspace root with
`--workspace <root>`. `Codebase Graph: Index Workspace` rebuilds all individual
graphs first and the aggregate graph last.

## Main MCP tools

- `prepare_code_context`: preferred first call for code navigation; reports graph
  availability and recommends the next graph tool
- `discover_repositories`: report actual repository boundaries and index state
- `index_workspace`: rebuild every repository graph and the aggregate graph
- `index_repository`: rebuild one selected repository graph
- `search_graph`, `trace_path`, `query_graph`: inspect one repository
- `search_workspace_graph`, `query_workspace_graph`, `get_workspace_architecture`:
  inspect the combined graph and cross-repository dependencies
- `check_index_coverage`: report indexed file counts by language

The MCP initialization instructions and skill both teach agents to prefer graph
queries over recursive `rg`/`grep` for structural questions and to pass the opened
workspace as an absolute `workspace_path`. This avoids accidental indexing of a
plugin or scratch directory when a client launcher does not preserve its requested
working directory. Text search remains the right choice for exact literals,
uncovered content, and verification.

Generated graphs are local data. On every successful save, the plugin adds
`.codebase-graph/` to the repository's local `.git/info/exclude`, so generated
indexes do not appear in VS Code Source Control and no tracked file is changed.
Remove that local exclude entry only if the team intentionally wants to version them.
