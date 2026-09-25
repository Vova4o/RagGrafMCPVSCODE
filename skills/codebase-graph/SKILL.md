---
name: codebase-graph
description: Use proactively for coding, debugging, refactoring, code review, architecture, navigation, symbol location, dependency tracing, callers, callees, and impact analysis. Prefer the shared graph before recursive rg, grep, find, or broad file reads when discovering code structure; use text search for exact literals or uncovered content.
---

# Codebase Graph

The connected `codebase-graph` MCP server is the preferred semantic navigation
layer. Start structural exploration with `prepare_code_context`; it confirms that
the graph exists and identifies the correct repository tools.

## Decision rule

- Prefer graph tools when locating implementations, understanding architecture,
  tracing callers/callees, finding dependencies, reviewing changes, debugging an
  unfamiliar path, or estimating impact.
- Prefer `search_code` or `rg`/`grep` for exact strings, configuration values,
  log messages, generated content, or languages missing from index coverage.
- After graph navigation, read the returned snippet or source file before editing.
- When the exact file and edit location are already known, do not add a graph call
  that would provide no useful context.

## Graph ownership

- Every actual Git repository owns exactly one `.codebase-graph/graph.json`.
- A workspace containing one or more repositories owns one aggregate
  `.codebase-graph/workspace.json`.
- Codex, Claude Code, Antigravity, and VS Code share these files.
- Never create agent-specific or client-specific graph copies.
- Graph saves add `.codebase-graph/` to local `.git/info/exclude`; do not add
  generated graph files to commits.

## Workflow

1. Call `prepare_code_context` for an opened workspace and always pass its absolute path as
   `workspace_path`. Never infer the workspace from the MCP process working directory: clients
   may launch the server from a plugin installation or a scratch directory. The call also audits
   existing `.mcp.json` and `.agents/mcp_config.json` entries. It replaces missing or versioned
   plugin-cache commands with a stable workspace-local server binary. Confirm that its
   `server_version` and `graph_schema_version` fields are present, and report any paths listed in
   `configuration_health.repaired`; affected clients need a restart.
   If the tool is absent or reports a schema mismatch, stop structural exploration
   and tell the user to restart the client in a new session; do not silently replace
   relationship analysis with recursive grep.
2. Call `index_workspace` only when graphs are missing or relevant source changes
   make them stale.
3. Use `search_workspace_graph` and `query_workspace_graph` to identify repository
   boundaries and `DEPENDS_ON` relationships.
4. For detailed work, pass the selected absolute `repo_path` to repository tools.
5. Use `search_graph` to resolve a symbol, then `trace_path` for callers/callees.
6. Use `get_code_snippet` for source evidence.
7. Call `check_index_coverage` before negative or exhaustive claims and disclose
   skipped files.

## Tool selection

- Repository discovery: `discover_repositories`
- First-call graph guidance: `prepare_code_context`
- Rebuild all shared graphs: `index_workspace`
- Cross-repository dependencies: `query_workspace_graph` with `edge: "DEPENDS_ON"`
- Workspace overview: `get_workspace_architecture`
- Declarations: `search_graph`
- Callers/callees: `trace_path` with `edge_kinds: ["CALLS"]`
- Class callers/callees: trace the class's `CONTAINS` methods, then inspect their
  `CALLS` edges; class-level call results aggregate calls made by methods.
- Imports: `query_graph` with `edge: "IMPORTS"`
- Exact source: `get_code_snippet`
- Literal source search: `search_code`

## Accuracy boundaries

- Go syntax is parsed with the Go AST and uses Go-specific import and call
  resolution.
- JavaScript, TypeScript, TSX, Python, Rust, Java, Kotlin, and C# use pure-Go
  syntax-tree extraction for declarations, methods, imports, direct calls, and
  exact source line ranges. JavaScript/TypeScript parser recovery is bounded:
  declarations and references inside syntax-tree `ERROR` or `MISSING` regions
  are skipped, while recoverable regions outside them can still be indexed.
  Type nodes contain their methods; class call traces aggregate method calls.
  Constructor calls are recorded, including calls at file scope. Literal
  JavaScript/TypeScript `import()` and `require()` paths are indexed as imports.
  Relative JavaScript/TypeScript and Python imports can resolve to same-repository
  files when unambiguous; other imports remain external unless covered by those
  rules.
- JavaScript/TypeScript calls through statically typed or inferred class fields
  can resolve when the field's class is known. Resolution remains conservative:
  ambiguous or unresolved targets stay external, and static analysis does not
  promise dynamic dispatch, reflection, or runtime loading resolution.
- SQL, shell, Ruby, PHP, C, C++, Protocol Buffers, Vue, Svelte, HTML, CSS, SCSS,
  YAML, TOML, JSON, XML, Dockerfile, and Makefile use older regex and file-level
  extraction. Do not assume syntax-tree-level declarations, call structure, or
  resolution for these languages.
- Syntax parser failures and syntax-parsed source files larger than 4 MiB are
  reported as skipped in index coverage. Generated `dist/` and `out/` trees are
  excluded, as are hidden directories, nested repositories, vendor trees,
  `node_modules`, build output, and `.codebase-graph`.
- `DEPENDS_ON` is inferred from indexed module imports or explicit service URLs
  matching another repository's identity. Treat it as source evidence, not proof
  that runtime traffic actually occurred.

## Safety

- `prepare_code_context` may update only existing codebase-graph entries in `.mcp.json` and
  `.agents/mcp_config.json`, plus the managed `.codebase-graph/bin/codebase-graph-mcp` copy.
  It preserves unrelated server entries and top-level configuration fields.
- `delete_project` deletes only the selected repository's `graph.json`.
- Do not delete source or client configuration through graph tools.
- Do not start background watchers or daemons.
