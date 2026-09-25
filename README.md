# Codebase Graph

`codebase-graph` is a local multi-language code graph shared by Codex, Claude Code,
Google Antigravity, and VS Code. It has no daemon and no per-agent databases.

## Install for VS Code

[Install Codebase Graph from the Visual Studio Marketplace](https://marketplace.visualstudio.com/items?itemName=vova4o.codebase-graph)

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

The indexer recognizes the languages below. Support is intentionally described by
the parser and graph facts currently available; it does not imply equal parse quality
or language parity.

| Languages | Current indexing capability |
| --- | --- |
| Go | Standard-library AST parsing for declarations, imports, and calls, with Go-specific symbol resolution. |
| JavaScript, TypeScript, TSX, Python, Rust, Java, Kotlin, C# | Pure-Go syntax-tree parsing for declarations, methods, imports, calls, and exact source line ranges. A small amount of parser recovery is tolerated so valid regions of mildly malformed files can still be indexed; files with extensive syntax errors are reported as skipped. |
| SQL, shell, Ruby, PHP, C, C++, Protocol Buffers, Vue, Svelte, HTML, CSS, SCSS, YAML, TOML, JSON, XML, Dockerfile, Makefile | Older regex and file-level extraction for limited declarations, imports, dependencies, and direct-call patterns; these do not provide syntax-tree-level structure or resolution. |

Calls resolve conservatively. Direct calls can resolve to declarations in the same
file, while unambiguous relative JavaScript, TypeScript, and Python imports can
resolve to files in the same repository. JavaScript and TypeScript constructor calls
such as `new Service()` are indexed as calls and can resolve to a known type. Literal
JavaScript `import()` and `require()` paths are recorded as imports; only paths that
match a same-repository file unambiguously resolve to that file. For JavaScript and
TypeScript class methods, statically visible typed fields and fields initialized with
`new Type()` can connect `this.field.method()` calls to methods of that type.

These rules do not model arbitrary dependency-injection containers, field mutation,
computed import paths, dynamic dispatch, reflection, runtime loading, or ambiguous
matches; unresolved targets remain external. Parser recovery accepts only a limited
number and span of syntax errors. Files with extensive syntax errors or source files
larger than 4 MiB are reported as skipped in index coverage. Generated `dist/` and
`out/` directories are excluded from indexing to avoid duplicate or compiled copies
of source files.

### Roadmap

- Add grammar-based syntax-tree extraction for the remaining indexed languages.
- Extend dependency-injection and cross-file symbol resolution while keeping
  uncertain targets explicit; runtime behavior and computed paths require more
  information than static indexing can provide.

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
The generated MCP entries point to
`<workspace>/.codebase-graph/bin/codebase-graph-mcp`, not to a versioned plugin
cache directory. The setup command refreshes that stable binary atomically.
The installer first performs a real MCP `initialize` and `tools/list` handshake;
it refuses to write configuration unless the graph-first tools are advertised.
Its result contains `restart_required: true`: existing client sessions keep their
old MCP process and must be replaced with a new session after an update.

Every `prepare_code_context` call also audits existing `.mcp.json` and
`.agents/mcp_config.json` entries. Missing commands and commands inside a
versioned plugin cache are migrated automatically to the stable workspace binary,
while unrelated configuration is preserved. Repaired clients must be restarted.
If a stale command prevents the only configured server from starting, the
packaged setup binary provides the same migration without hand-editing JSON:

```bash
./bin/codebase-graph-setup --action repair --workspace /absolute/path/to/workspace
```

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
