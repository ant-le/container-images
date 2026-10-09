package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owncloud/ocis-mcp-server/internal/client"
	"github.com/owncloud/ocis-mcp-server/internal/config"
)

const deleteSpace = "storage$output"
const deleteID = deleteSpace + "!file-node"

func deleteFixture(t *testing.T, path, mode string) (*companyFiles, *int, *bool) {
	t.Helper()
	exists, deletes := true, 0
	escape := func(s string) string { var b strings.Builder; xml.EscapeText(&b, []byte(s)); return b.String() }
	metadata := func(id string) string {
		typeXML := ""
		if mode == "folder" {
			typeXML = "<d:collection/>"
		}
		href := (&url.URL{Path: "/dav/spaces/" + deleteSpace + "/" + path}).EscapedPath()
		return fmt.Sprintf(`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype>%s</d:resourcetype><oc:fileid>%s</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, escape(href), typeXML, escape(id))
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "agent" || token != "synthetic" {
			t.Error("missing authenticated identity")
			w.WriteHeader(401)
			return
		}
		if r.Method == "PROPFIND" && r.Header.Get("Depth") != "0" {
			t.Error("nonzero deletion metadata depth")
		}
		idPath := "/dav/spaces/" + deleteID
		if r.Method == "DELETE" {
			deletes++
			if r.URL.Path != idPath {
				t.Error("DELETE addressed a pathname or unverified ID")
			}
			if r.Body != nil && r.ContentLength != 0 {
				t.Error("nonempty DELETE body")
			}
			if r.Header.Get("If-Match") != "" {
				t.Error("unsupported CAS guarantee")
			}
			if mode == "delete-forbidden" {
				w.WriteHeader(403)
				return
			}
			exists = false
			if mode == "lost-response" {
				connection, _, _ := w.(http.Hijacker).Hijack()
				connection.Close()
				return
			}
			w.WriteHeader(204)
			return
		}
		if !exists {
			if mode == "readback-failed" {
				w.WriteHeader(500)
			} else {
				w.WriteHeader(404)
			}
			return
		}
		if r.Method == "GET" && r.URL.Path == "/graph/v1.0/drives/"+deleteSpace+"/items/"+deleteID {
			// The path may be missing while the original ID still exists elsewhere.
			if mode == "expired-token" {
				w.WriteHeader(401)
				return
			}
			item := map[string]any{"id": deleteID, "file": map[string]string{"mimeType": "application/octet-stream"}, "parentReference": map[string]string{"driveId": deleteSpace}}
			switch mode {
			case "non-file":
				delete(item, "file")
			case "graph-folder":
				item["folder"] = map[string]any{}
			case "graph-other-id":
				item["id"] = deleteSpace + "!other-node"
			case "graph-other-space":
				item["parentReference"] = map[string]string{"driveId": "storage$canonical"}
			case "remote-mount":
				item["remoteItem"] = map[string]any{"id": "remote"}
			case "path-replaced":
				mode = "folder"
			}
			json.NewEncoder(w).Encode(item)
			return
		}
		if mode == "moved-path" && r.Method == "PROPFIND" {
			if r.URL.Path == idPath {
				w.WriteHeader(207)
				fmt.Fprint(w, metadata(deleteID))
			} else {
				w.WriteHeader(404)
			}
			return
		}
		if r.Method != "PROPFIND" || r.URL.Path != "/dav/spaces/"+deleteSpace+"/"+path {
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		data := metadata(deleteID)
		switch mode {
		case "stale-id":
			data = metadata(deleteSpace + "!replacement-node")
		case "metadata-other-space":
			data = metadata("storage$canonical!file-node")
		case "wrong-href":
			data = strings.Replace(data, escape(path), "different-path", 1)
		case "missing-type":
			data = strings.Replace(data, "<d:resourcetype></d:resourcetype>", "", 1)
		case "denied-property":
			data = strings.Replace(data, "200 OK", "403 Forbidden", 1)
		case "wrong-namespace":
			data = strings.ReplaceAll(data, "DAV:", "not-DAV:")
		case "oversize-metadata":
			data = strings.Repeat("x", 65537)
		case "malformed-metadata":
			data = "broken"
		case "redirect":
			w.Header().Set("Location", "https://outside.invalid/private")
			w.WriteHeader(307)
			return
		}
		w.WriteHeader(207)
		fmt.Fprint(w, data)
	}))
	t.Cleanup(srv.Close)
	f := &companyFiles{cfg: &config.Config{OcisURL: srv.URL, AppTokenUser: "agent", AppTokenValue: "synthetic"}, http: srv.Client(), writeSpace: deleteSpace, readSpaces: map[string]bool{deleteSpace: true, "storage$canonical": true}, allowDelete: true}
	f.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return f, &deletes, &exists
}

func TestCompanyDeleteExactFileIDAndRetry(t *testing.T) {
	for _, path := range []string{"report.md", "reports/result.xlsx", "Agenda #1?/résumé.pdf"} {
		t.Run(path, func(t *testing.T) {
			f, deletes, exists := deleteFixture(t, path, "")
			in := companyDelete{deleteSpace, path, deleteID}
			_, out, err := f.deleteFile(context.Background(), nil, in)
			if err != nil || out.Status != "trashed" || out.Path != path || out.FileID != deleteID || *exists || *deletes != 1 {
				t.Fatal("file deletion not confirmed", out, err)
			}
			_, out, err = f.deleteFile(context.Background(), nil, in)
			if err != nil || out.Status != "already_absent" || *deletes != 1 {
				t.Fatal("retry was not idempotent", out, err)
			}
		})
	}
}

func TestCompanyDeleteRejectsMetadataAndNonFiles(t *testing.T) {
	for _, mode := range []string{"folder", "stale-id", "metadata-other-space", "wrong-href", "missing-type", "denied-property", "wrong-namespace", "oversize-metadata", "malformed-metadata", "redirect", "non-file", "graph-folder", "graph-other-id", "graph-other-space", "remote-mount", "path-replaced", "moved-path", "expired-token"} {
		t.Run(mode, func(t *testing.T) {
			f, deletes, exists := deleteFixture(t, "report.xlsx", mode)
			if _, _, err := f.deleteFile(context.Background(), nil, companyDelete{deleteSpace, "report.xlsx", deleteID}); err == nil || *deletes != 0 || !*exists {
				t.Fatal("unsafe deletion reached mutation", err, *deletes)
			}
		})
	}
}

func TestCompanyDeleteRejectsScopeAndPathsWithoutNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer srv.Close()
	f := &companyFiles{cfg: &config.Config{OcisURL: srv.URL}, http: srv.Client(), writeSpace: deleteSpace, readSpaces: map[string]bool{deleteSpace: true, "storage$canonical": true}, allowDelete: true}
	ctx := context.Background()
	inputs := []companyDelete{{"storage$canonical", "report.md", "storage$canonical!node"}, {"storage$other", "report.md", "storage$other!node"}, {deleteSpace, "report.md", deleteSpace + "!output"}, {deleteSpace, "report.md", "storage$canonical!node"}}
	for _, path := range []string{"", "/", "/x", "../x", "x/../y", "x//y", "x/", "x%2fy", "x\\y"} {
		inputs = append(inputs, companyDelete{deleteSpace, path, deleteID})
	}
	for _, id := range []string{"", "*", deleteSpace, deleteSpace + "!node/path", deleteSpace + "!%2e", deleteSpace + "!x!y"} {
		inputs = append(inputs, companyDelete{deleteSpace, "report.md", id})
	}
	for _, in := range inputs {
		if _, _, err := f.deleteFile(ctx, nil, in); err == nil {
			t.Fatal("invalid deletion accepted", in)
		}
	}
	f.allowDelete = false
	if _, _, err := f.deleteFile(ctx, nil, companyDelete{deleteSpace, "report.md", deleteID}); err == nil {
		t.Fatal("disabled deletion accepted")
	}
	f.allowDelete = true
	f.writeSpace = ""
	if _, _, err := f.deleteFile(ctx, nil, companyDelete{deleteSpace, "report.md", deleteID}); err == nil {
		t.Fatal("read-only profile deleted")
	}
	if requests != 0 {
		t.Fatal("denied requests reached backend")
	}
}

func TestCompanyDeleteAmbiguousFailureDoesNotRetry(t *testing.T) {
	for _, mode := range []string{"lost-response", "readback-failed", "delete-forbidden"} {
		t.Run(mode, func(t *testing.T) {
			f, deletes, _ := deleteFixture(t, "report.xlsx", mode)
			_, _, err := f.deleteFile(context.Background(), nil, companyDelete{deleteSpace, "report.xlsx", deleteID})
			if err == nil || *deletes != 1 || !strings.Contains(err.Error(), "inspect this path and ID before retry") {
				t.Fatal("ambiguous deletion retried or hidden", err, *deletes)
			}
			if mode == "lost-response" {
				_, out, err := f.deleteFile(context.Background(), nil, companyDelete{deleteSpace, "report.xlsx", deleteID})
				if err != nil || out.Status != "already_absent" || *deletes != 1 {
					t.Fatal("explicit retry after lost response failed", out, err)
				}
			}
		})
	}
}

func TestCompanyDeleteToolOptInAndDestructiveHint(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			t.Setenv("OCIS_MCP_PROFILE", "catalog")
			t.Setenv("OCIS_MCP_ALLOW_DELETE", fmt.Sprint(allow))
			t.Setenv("OCIS_MCP_WRITE_SPACE_ID", deleteSpace)
			t.Setenv("OCIS_MCP_READ_SPACE_IDS", `["storage$output","storage$canonical"]`)
			cfg := &config.Config{OcisURL: "https://example.invalid", Transport: "stdio", AuthMode: "app-token", AppTokenUser: "agent", AppTokenValue: "synthetic", HTTPTimeout: time.Second}
			s := mcp.NewServer(&mcp.Implementation{Name: "deletion-test", Version: "1"}, nil)
			if err := RegisterCompany(s, client.New(cfg), cfg); err != nil {
				t.Fatal(err)
			}
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
			list, err := cs.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range list.Tools {
				if tool.Name == "ocis_delete_file" {
					found = true
					if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint || tool.Annotations.ReadOnlyHint {
						t.Fatal("delete tool hides destructive nature")
					}
				}
			}
			if found != allow {
				t.Fatal("delete opt-in not respected")
			}
			result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ocis_delete_file", Arguments: map[string]any{"space_id": "storage$canonical", "path": "report.md", "file_id": "storage$canonical!node"}})
			if err == nil && (result == nil || !result.IsError) {
				t.Fatal("unregistered or forbidden mutation callable")
			}
		})
	}
}
