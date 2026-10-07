package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owncloud/ocis-mcp-server/internal/client"
)

// Policy is administrator-owned, outside every agent workspace. Credentials
// identify the backend user; this additional allowlist never grants backend access.
type agentScope struct {
	Username     string   `json:"username"`
	ReadSpaceIDs []string `json:"read_space_ids"`
	WriteSpaceID string   `json:"write_space_id"`
	Sandbox      string   `json:"sandbox"`
}

func configureScope() error {
	path := os.Getenv("OCIS_MCP_POLICY_FILE")
	if path == "" {
		return nil
	} // Explicit environment remains supported for local tests.
	file, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open agent scope policy")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("invalid agent scope policy")
	}
	var scopes map[string]agentScope
	if json.Unmarshal(data, &scopes) != nil {
		return errors.New("invalid agent scope policy JSON")
	}
	scope, ok := scopes[os.Getenv("OCIS_MCP_AGENT")]
	if !ok || scope.Username == "" || scope.Username != os.Getenv("OCIS_MCP_APP_TOKEN_USER") {
		return errors.New("agent scope missing or credential username mismatch")
	}
	ids, _ := json.Marshal(scope.ReadSpaceIDs)
	for key, value := range map[string]string{
		"OCIS_MCP_READ_SPACE_IDS": string(ids), "OCIS_MCP_WRITE_SPACE_ID": scope.WriteSpaceID, "OCIS_MCP_SANDBOX": scope.Sandbox,
	} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func parseReadSpaces(raw string) (map[string]bool, error) {
	var ids []string
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &ids) != nil || len(ids) == 0 || len(ids) > 64 {
		return nil, errors.New("read_space_ids must contain 1 to 64 explicit Space IDs")
	}
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, err := escapedDAV(id, "scope-check"); err != nil || id == "." || id == ".." || id == "*" {
			return nil, errors.New("invalid allowed Space ID")
		}
		allowed[id] = true
	}
	return allowed, nil
}

func scopedHandler[I, O any](f *companyFiles, space func(I) string, next mcp.ToolHandlerFor[I, O]) mcp.ToolHandlerFor[I, O] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in I) (*mcp.CallToolResult, O, error) {
		if !f.readSpaces[space(in)] {
			var empty O
			return nil, empty, errors.New("Space is outside this agent's configured scope; select an allowed space_id")
		}
		return next(ctx, req, in)
	}
}

func (f *companyFiles) registerCatalog(s *mcp.Server, c *client.Client) {
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_list_my_spaces", Description: "List this agent's allowed Spaces that the backend account can access. Pagination follows backend pages; an empty filtered page may still have more pages.", Annotations: readOnlyAnnotations()},
		func(ctx context.Context, req *mcp.CallToolRequest, in ListSpacesInput) (*mcp.CallToolResult, ListSpacesOutput, error) {
			_, out, err := handleListSpaces(c, "/graph/v1.0/me/drives")(ctx, req, in)
			if err != nil {
				return nil, ListSpacesOutput{}, err
			}
			filtered := make([]Drive, 0)
			for _, drive := range out.Spaces {
				if f.readSpaces[drive.ID] {
					filtered = append(filtered, drive)
				}
			}
			out.Spaces, out.TotalCount = filtered, len(filtered)
			return nil, out, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_list_files", Description: "List files in one allowed Space.", Annotations: readOnlyAnnotations()},
		scopedHandler(f, func(in ListFilesInput) string { return in.SpaceID }, func(ctx context.Context, req *mcp.CallToolRequest, in ListFilesInput) (*mcp.CallToolResult, ListFilesOutput, error) {
			path, err := catalogDAV(in.SpaceID, in.Path)
			if err != nil {
				return nil, ListFilesOutput{}, err
			}
			ms, err := client.Propfind(ctx, c, path, "1")
			if err != nil {
				return nil, ListFilesOutput{}, err
			}
			files := make([]client.FileInfo, 0)
			for i, response := range ms.Responses {
				if i > 0 {
					files = append(files, response.ToFileInfo())
				}
			}
			return nil, ListFilesOutput{Files: files, Path: in.Path, Count: len(files)}, nil
		}))
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_get_file_info", Description: "Get file metadata in one allowed Space.", Annotations: readOnlyAnnotations()},
		scopedHandler(f, func(in GetFileInfoInput) string { return in.SpaceID }, func(ctx context.Context, req *mcp.CallToolRequest, in GetFileInfoInput) (*mcp.CallToolResult, GetFileInfoOutput, error) {
			path, err := catalogDAV(in.SpaceID, in.Path)
			if err != nil {
				return nil, GetFileInfoOutput{}, err
			}
			ms, err := client.Propfind(ctx, c, path, "0")
			if err != nil {
				return nil, GetFileInfoOutput{}, err
			}
			if len(ms.Responses) == 0 {
				return nil, GetFileInfoOutput{}, errors.New("no metadata returned")
			}
			return nil, GetFileInfoOutput{FileInfo: ms.Responses[0].ToFileInfo()}, nil
		}))
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_search", Description: "Search filenames and metadata in one explicit allowed space_id (required). No global search or document extraction.", Annotations: readOnlyAnnotations()},
		scopedHandler(f, func(in SearchInput) string { return in.SpaceID }, func(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
			_, out, err := handleSearch(c)(ctx, req, in)
			if err != nil {
				return nil, SearchOutput{}, err
			}
			// Fail closed for results with missing or unexpected Space provenance.
			filtered := make([]SearchResult, 0)
			for _, result := range out.Results {
				if result.SpaceID == in.SpaceID {
					filtered = append(filtered, result)
				}
			}
			out.Results, out.TotalCount = filtered, len(filtered)
			return nil, out, nil
		}))
}

func catalogDAV(space, path string) (string, error) {
	if path == "" || path == "/" {
		root, err := escapedDAV(space, "scope-check")
		return strings.TrimSuffix(root, "scope-check"), err
	}
	// Model paths are literal relative names, never pre-encoded URLs. Match
	// byte-transfer validation, then escape each segment before HTTP construction.
	return escapedDAV(space, path)
}
