// Package mcp implements the stdio Model Context Protocol transport.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/vladimirgavrilenko/codebase-graph/internal/graph"
	"github.com/vladimirgavrilenko/codebase-graph/internal/service"
)

const (
	serverName    = "codebase-graph"
	serverVersion = "0.2.4"
	protocol      = "2025-06-18"
)

// Server exposes graph operations over JSON-RPC stdio.
type Server struct {
	service *service.Service
	input   io.Reader
	output  io.Writer
}

// NewServer returns an MCP stdio server.
func NewServer(graphService *service.Service, input io.Reader, output io.Writer) *Server {
	return &Server{service: graphService, input: input, output: output}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Run serves requests until stdin closes or the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.input)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	writer := bufio.NewWriter(s.output)
	defer writer.Flush()

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("serve MCP requests: %w", err)
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var message request
		if err := json.Unmarshal(line, &message); err != nil {
			if writeErr := writeResponse(writer, response{
				JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: "invalid JSON-RPC message"},
			}); writeErr != nil {
				return writeErr
			}
			continue
		}

		result, rpcErr := s.handle(ctx, message)
		if len(message.ID) == 0 {
			continue
		}
		out := response{JSONRPC: "2.0", ID: message.ID, Result: result, Error: rpcErr}
		if writeErr := writeResponse(writer, out); writeErr != nil {
			return writeErr
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, message request) (any, *rpcError) {
	switch message.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocol,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo":   map[string]any{"name": serverName, "version": serverVersion},
			"instructions": "A shared code graph is available. For coding, debugging, refactoring, review, architecture, symbol location, callers, dependencies, or impact analysis, call prepare_code_context before broad repository exploration and pass the absolute path of the opened workspace as workspace_path. Never infer the workspace from the MCP process working directory because launchers may start the server in a plugin or scratch directory. Then use graph tools as the primary source for structural discovery. Use search_code or grep only for exact literals, unsupported content, coverage gaps, or verification after graph results. Before negative or exhaustive source claims, call check_index_coverage and disclose skipped files. Do not rebuild an existing graph unless source changes make it stale.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		var params callParams
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid tools/call parameters"}
		}
		return s.callTool(ctx, params), nil
	case "notifications/initialized", "notifications/cancelled", "notifications/roots/list_changed":
		return nil, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callTool(ctx context.Context, params callParams) toolResult {
	var value any
	var err error

	switch params.Name {
	case "prepare_code_context":
		var args workspaceArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			var prepared service.CodeContextResult
			prepared, err = s.service.PrepareCodeContext(ctx, args.WorkspacePath)
			value = preparedCodeContext{
				CodeContextResult:  prepared,
				ServerVersion:      serverVersion,
				GraphSchemaVersion: graph.SchemaVersion,
			}
		}
	case "discover_repositories":
		var args workspaceArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.DiscoverRepositories(ctx, args.WorkspacePath)
		}
	case "index_workspace":
		var args workspaceArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.IndexWorkspace(ctx, args.WorkspacePath)
		}
	case "index_repository":
		var args repoArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Index(ctx, args.RepoPath)
		}
	case "index_status":
		var args repoArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Status(args.RepoPath)
		}
	case "list_projects":
		var args workspaceArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.DiscoverRepositories(ctx, args.WorkspacePath)
		}
	case "search_workspace_graph":
		var args workspaceSearchArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.WorkspaceSearch(args.WorkspacePath, args.Query, args.Kind, args.Limit)
		}
	case "query_workspace_graph":
		var args workspaceGraphQueryArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.QueryWorkspaceGraph(args.WorkspacePath, args.From, args.Edge, args.To, args.Limit)
		}
	case "get_workspace_architecture":
		var args workspaceArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.WorkspaceArchitecture(args.WorkspacePath)
		}
	case "search_graph":
		var args searchArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Search(args.RepoPath, args.Query, args.Kind, args.Limit)
		}
	case "trace_path":
		var args traceArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Trace(args.RepoPath, args.Symbol, args.Direction, args.Depth, args.Limit, args.EdgeKinds)
		}
	case "get_code_snippet":
		var args snippetArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Snippet(args.RepoPath, args.Symbol, args.ContextLines)
		}
	case "get_architecture":
		var args repoArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Architecture(args.RepoPath)
		}
	case "check_index_coverage":
		var args repoArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.Coverage(args.RepoPath)
		}
	case "query_graph":
		var args graphQueryArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.QueryGraph(args.RepoPath, args.From, args.Edge, args.To, args.Limit)
		}
	case "search_code":
		var args codeSearchArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			value, err = s.service.SearchCode(args.RepoPath, args.Query, args.Limit)
		}
	case "delete_project":
		var args repoArgs
		if err = decodeArgs(params.Arguments, &args); err == nil {
			err = s.service.Delete(ctx, args.RepoPath)
			value = map[string]any{"deleted": err == nil}
		}
	default:
		err = fmt.Errorf("unknown tool %s", params.Name)
	}

	if err != nil {
		return toolResult{Content: []content{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	payload, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil {
		return toolResult{Content: []content{{Type: "text", Text: "encode tool result: " + marshalErr.Error()}}, IsError: true}
	}
	return toolResult{Content: []content{{Type: "text", Text: string(payload)}}}
}

type preparedCodeContext struct {
	service.CodeContextResult
	ServerVersion      string `json:"server_version"`
	GraphSchemaVersion int    `json:"graph_schema_version"`
}

type repoArgs struct {
	RepoPath string `json:"repo_path"`
}

type workspaceArgs struct {
	WorkspacePath string `json:"workspace_path"`
}

type workspaceSearchArgs struct {
	WorkspacePath string `json:"workspace_path"`
	Query         string `json:"query"`
	Kind          string `json:"kind"`
	Limit         int    `json:"limit"`
}

type workspaceGraphQueryArgs struct {
	WorkspacePath string `json:"workspace_path"`
	From          string `json:"from"`
	Edge          string `json:"edge"`
	To            string `json:"to"`
	Limit         int    `json:"limit"`
}

type searchArgs struct {
	RepoPath string `json:"repo_path"`
	Query    string `json:"query"`
	Kind     string `json:"kind"`
	Limit    int    `json:"limit"`
}

type traceArgs struct {
	RepoPath  string   `json:"repo_path"`
	Symbol    string   `json:"symbol"`
	Direction string   `json:"direction"`
	Depth     int      `json:"depth"`
	Limit     int      `json:"limit"`
	EdgeKinds []string `json:"edge_kinds"`
}

type snippetArgs struct {
	RepoPath     string `json:"repo_path"`
	Symbol       string `json:"symbol"`
	ContextLines int    `json:"context_lines"`
}

type graphQueryArgs struct {
	RepoPath string `json:"repo_path"`
	From     string `json:"from"`
	Edge     string `json:"edge"`
	To       string `json:"to"`
	Limit    int    `json:"limit"`
}

type codeSearchArgs struct {
	RepoPath string `json:"repo_path"`
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
}

func decodeArgs(payload json.RawMessage, target any) error {
	if len(payload) == 0 || string(payload) == "null" {
		payload = json.RawMessage("{}")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	return nil
}

func writeResponse(writer *bufio.Writer, value response) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode MCP response: %w", err)
	}
	if _, err := writer.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write MCP response: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush MCP response: %w", err)
	}
	return nil
}

func tools() []tool {
	repoProperty := map[string]any{
		"type":        "string",
		"description": "Absolute repository path. Optional when the server was started with --repo.",
	}
	workspaceProperty := map[string]any{
		"type":        "string",
		"description": "Absolute path of the opened workspace. Required unless the server was started with --workspace; never substitute the MCP process working directory.",
	}
	limitProperty := map[string]any{"type": "integer", "minimum": 1}
	return []tool{
		{Name: "prepare_code_context", Description: "Required first call before broad repository exploration for coding, debugging, refactoring, review, architecture, symbol navigation, dependency tracing, and impact analysis. Pass the absolute opened workspace as workspace_path because the MCP process may run from a plugin or scratch directory. Confirms shared graph availability and selects graph-first next steps; text search remains appropriate for exact literals or uncovered content.", InputSchema: objectSchema(map[string]any{"workspace_path": workspaceProperty})},
		{Name: "discover_repositories", Description: "Find actual Git repositories inside a workspace and report their individual graph paths.", InputSchema: objectSchema(map[string]any{"workspace_path": workspaceProperty})},
		{Name: "index_workspace", Description: "Build one graph per repository, then build one aggregate workspace graph with cross-repository dependencies.", InputSchema: objectSchema(map[string]any{"workspace_path": workspaceProperty})},
		{Name: "index_repository", Description: "Build or replace the shared multi-language graph stored inside one repository.", InputSchema: objectSchema(map[string]any{"repo_path": repoProperty})},
		{Name: "index_status", Description: "Report whether the repository-local graph exists and when it was indexed.", InputSchema: objectSchema(map[string]any{"repo_path": repoProperty})},
		{Name: "list_projects", Description: "List repositories in the workspace and the status of each individual graph.", InputSchema: objectSchema(map[string]any{"workspace_path": workspaceProperty})},
		{Name: "search_workspace_graph", Description: "Preferred semantic search for repositories, symbols, declarations, and files across the aggregate workspace graph; use this before recursive grep when locating code structure.", InputSchema: objectSchema(map[string]any{
			"workspace_path": workspaceProperty, "query": map[string]any{"type": "string"}, "kind": nodeKindProperty(), "limit": limitProperty,
		}, "query")},
		{Name: "query_workspace_graph", Description: "Filter aggregate relationships, including DEPENDS_ON links between repositories.", InputSchema: objectSchema(map[string]any{
			"workspace_path": workspaceProperty, "from": map[string]any{"type": "string"}, "edge": edgeKindProperty(), "to": map[string]any{"type": "string"}, "limit": limitProperty,
		})},
		{Name: "get_workspace_architecture", Description: "Summarize the aggregate workspace graph and cross-repository dependencies.", InputSchema: objectSchema(map[string]any{"workspace_path": workspaceProperty})},
		{Name: "search_graph", Description: "Preferred repository-level search for symbols, declarations, and structural nodes; use before grep when locating code by meaning or symbol name.", InputSchema: objectSchema(map[string]any{
			"repo_path": repoProperty,
			"query":     map[string]any{"type": "string"},
			"kind":      nodeKindProperty(),
			"limit":     limitProperty,
		}, "query")},
		{Name: "trace_path", Description: "Preferred tool for callers, callees, dependencies, and change-impact paths; grep cannot reliably establish these relationships.", InputSchema: objectSchema(map[string]any{
			"repo_path":  repoProperty,
			"symbol":     map[string]any{"type": "string"},
			"direction":  map[string]any{"type": "string", "enum": []string{"inbound", "outbound", "both"}},
			"depth":      map[string]any{"type": "integer", "minimum": 1, "maximum": 8},
			"limit":      limitProperty,
			"edge_kinds": map[string]any{"type": "array", "items": edgeKindProperty()},
		}, "symbol")},
		{Name: "get_code_snippet", Description: "Read the current source range for one indexed symbol.", InputSchema: objectSchema(map[string]any{
			"repo_path":     repoProperty,
			"symbol":        map[string]any{"type": "string"},
			"context_lines": map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
		}, "symbol")},
		{Name: "get_architecture", Description: "Summarize packages, node kinds, edge kinds, and call fan-in/fan-out.", InputSchema: objectSchema(map[string]any{"repo_path": repoProperty})},
		{Name: "check_index_coverage", Description: "Show indexed source-file counts per language and parse failures.", InputSchema: objectSchema(map[string]any{"repo_path": repoProperty})},
		{Name: "query_graph", Description: "Filter graph edges by source text, edge kind, and target text.", InputSchema: objectSchema(map[string]any{
			"repo_path": repoProperty,
			"from":      map[string]any{"type": "string"},
			"edge":      edgeKindProperty(),
			"to":        map[string]any{"type": "string"},
			"limit":     limitProperty,
		})},
		{Name: "search_code", Description: "Search literal text in source files recorded by the repository graph.", InputSchema: objectSchema(map[string]any{
			"repo_path": repoProperty,
			"query":     map[string]any{"type": "string"},
			"limit":     limitProperty,
		}, "query")},
		{Name: "delete_project", Description: "Delete only the graph.json index inside the selected repository.", InputSchema: objectSchema(map[string]any{"repo_path": repoProperty})},
	}
}

func nodeKindProperty() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"Workspace", "Repository", "Project", "Package", "File", "Function", "Method", "Type", "External"}}
}

func edgeKindProperty() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"CONTAINS", "DEFINES", "IMPORTS", "CALLS", "REFERENCES", "DEPENDS_ON"}}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
