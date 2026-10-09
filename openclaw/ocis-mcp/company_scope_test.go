package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owncloud/ocis-mcp-server/internal/client"
	"github.com/owncloud/ocis-mcp-server/internal/config"
)

func TestCompanyPolicyBindsIdentityAndClearsStaleScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"workshop":{"username":"agent-workshop","read_space_ids":["workshops"],"write_space_id":"","sandbox":"workshop-fixed"},"stocks":{"username":"agent-stocks","read_space_ids":["stocks"],"write_space_id":"stocks","allow_delete":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCIS_MCP_POLICY_FILE", path)
	t.Setenv("OCIS_MCP_AGENT", "workshop")
	t.Setenv("OCIS_MCP_APP_TOKEN_USER", "agent-stocks")
	t.Setenv("OCIS_MCP_READ_SPACE_IDS", `["old"]`)
	t.Setenv("OCIS_MCP_WRITE_SPACE_ID", "old")
	t.Setenv("OCIS_MCP_SANDBOX", "old")
	t.Setenv("OCIS_MCP_ALLOW_DELETE", "true")
	if configureScope() == nil {
		t.Fatal("cross-agent credential accepted")
	}
	t.Setenv("OCIS_MCP_APP_TOKEN_USER", "agent-workshop")
	if err := configureScope(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OCIS_MCP_READ_SPACE_IDS") != `["workshops"]` || os.Getenv("OCIS_MCP_WRITE_SPACE_ID") != "" || os.Getenv("OCIS_MCP_SANDBOX") != "workshop-fixed" || os.Getenv("OCIS_MCP_ALLOW_DELETE") != "false" {
		t.Fatal("stale scope or wrong binding")
	}
	t.Setenv("OCIS_MCP_AGENT", "stocks")
	t.Setenv("OCIS_MCP_APP_TOKEN_USER", "agent-stocks")
	if err := configureScope(); err != nil || os.Getenv("OCIS_MCP_ALLOW_DELETE") != "true" {
		t.Fatal("administrator delete opt-in was not loaded", err)
	}
	t.Setenv("OCIS_MCP_AGENT", "unknown")
	if configureScope() == nil {
		t.Fatal("unknown agent accepted")
	}
	for _, raw := range []string{"", "null", "[]", `["*"]`, `[".."]`, `["x/y"]`} {
		if _, err := parseReadSpaces(raw); err == nil {
			t.Fatalf("unsafe scope accepted: %q", raw)
		}
	}
}

func TestCompanyCatalogAndBytesEnforceSameScope(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/graph/v1.0/me/drives" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"value":[{"id":"allowed","name":"Allowed"},{"id":"private","name":"Hidden"}]}`))
			return
		}
		if r.Method == "REPORT" {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			w.Write([]byte(`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>/dav/spaces/private/secret.txt</d:href><d:propstat><d:prop><d:displayname>secret.txt</d:displayname><oc:spaceid>private</oc:spaceid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	f := &companyFiles{readSpaces: map[string]bool{"allowed": true}, cfg: &config.Config{OcisURL: srv.URL}, http: srv.Client(), gate: make(chan struct{}, 1)}
	cfg := &config.Config{OcisURL: srv.URL, AuthMode: "app-token", AppTokenUser: "test", AppTokenValue: "test", HTTPTimeout: time.Second}
	s := mcp.NewServer(&mcp.Implementation{Name: "scope-test", Version: "1"}, nil)
	f.registerCatalog(s, client.New(cfg))
	a, b := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := s.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ocis_list_my_spaces", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatal("list failed", err, result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "Hidden") || strings.Contains(string(encoded), "private") || !strings.Contains(string(encoded), "Allowed") {
		t.Fatal("unscoped listing", string(encoded))
	}
	before := requests
	for _, name := range []string{"ocis_list_files", "ocis_get_file_info"} {
		for _, path := range []string{"%2e%2e/private/secret.txt", "%252e%252e/private/x", "../private/x", "x/../../private/x", "x\\y", "/private/x"} {
			result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"space_id": "allowed", "path": path}})
			if err != nil || !result.IsError {
				t.Fatalf("%s accepted unsafe path %q: %v", name, path, err)
			}
		}
	}
	for _, name := range []string{"ocis_list_files", "ocis_get_file_info", "ocis_search"} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"space_id": "private", "path": "secret.txt", "pattern": "secret"}})
		if err != nil || !result.IsError {
			t.Fatalf("%s did not deny cross-agent request: %v", name, err)
		}
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "ocis_search", Arguments: map[string]any{"pattern": "secret"}})
	if err != nil || !result.IsError {
		t.Fatal("global search accepted", err)
	}
	if _, _, err := f.readText(ctx, nil, companyPath{"private", "secret.txt"}); err == nil {
		t.Fatal("text scope bypass")
	}
	if _, _, err := f.stageFile(ctx, nil, companyPath{"private", "secret.pdf"}); err == nil {
		t.Fatal("binary scope bypass")
	}
	if requests != before {
		t.Fatal("denied operations reached backend")
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "ocis_search", Arguments: map[string]any{"space_id": "allowed", "pattern": "secret"}})
	if err != nil || result.IsError {
		t.Fatal("scoped search failed", err, result)
	}
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), "secret.txt") {
		t.Fatal("out-of-scope search result leaked")
	}
}

func TestCompanyCatalogPathEncodingAndPagination(t *testing.T) {
	for _, path := range []string{"", "/"} {
		got, err := catalogDAV("allowed", path)
		if err != nil || got != "/dav/spaces/allowed/" {
			t.Fatal("root listing broken", got, err)
		}
	}
	got, err := catalogDAV("allowed", "Agenda #1?.md")
	if err != nil || got != "/dav/spaces/allowed/Agenda%20%231%3F.md" {
		t.Fatal("metadata path was not escaped", got, err)
	}
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if page == 0 && r.URL.Query().Get("$skip") != "" {
			t.Error("unexpected initial offset")
		}
		if page == 1 && r.URL.Query().Get("$skip") != "1" {
			t.Error("lost backend offset")
		}
		id := "private"
		if page == 1 {
			id = "allowed"
		}
		page++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"value": []map[string]string{{"id": id}}})
	}))
	defer srv.Close()
	f := &companyFiles{readSpaces: map[string]bool{"allowed": true}}
	cfg := &config.Config{OcisURL: srv.URL, AuthMode: "app-token", AppTokenUser: "test", AppTokenValue: "test", HTTPTimeout: time.Second}
	s := mcp.NewServer(&mcp.Implementation{Name: "pagination", Version: "1"}, nil)
	f.registerCatalog(s, client.New(cfg))
	a, b := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := s.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for offset := 0; offset < 2; offset++ {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ocis_list_my_spaces", Arguments: map[string]any{"limit": 1, "offset": offset}})
		if err != nil || result.IsError {
			t.Fatal("pagination call failed", err, result)
		}
		encoded, _ := json.Marshal(result.StructuredContent)
		var out ListSpacesOutput
		if err := json.Unmarshal(encoded, &out); err != nil {
			t.Fatal(err)
		}
		if !out.HasMore || out.NextOffset != offset+1 || out.TotalCount != offset || len(out.Spaces) != offset {
			t.Fatal("filtered page lost backend pagination", out)
		}
	}
}
