package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/owncloud/ocis-mcp-server/internal/config"
)

func TestCompanyTextExactPathsAndReplacement(t *testing.T) {
	f, files, _ := fixture(t)
	for _, destination := range []string{"report.md", "reports/2026/report.md", "Agenda #1?/Notes & plans.md"} {
		for _, content := range []string{"first version", "updated version", ""} {
			_, result, err := f.writeText(context.Background(), nil, companyText{"write", destination, content})
			if err != nil || result.Path != destination || result.Size != int64(len(content)) || result.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) {
				t.Fatal("exact-path write failed", destination, result, err)
			}
			if string((*files)["/dav/spaces/write/"+destination]) != content {
				t.Fatal("destination did not contain replacement bytes")
			}
		}
	}
	if len(*files) != 7 { // Input, three requested files and three parent folders.
		t.Fatalf("unexpected remote paths (random output folder?): %v", len(*files))
	}
}

func TestCompanyReplacementAccepts201And204(t *testing.T) {
	for _, status := range []int{201, 204} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stored := "old"
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "PUT":
					data, _ := io.ReadAll(r.Body)
					stored = string(data)
					w.WriteHeader(status)
				case "GET":
					io.WriteString(w, stored)
				default:
					t.Errorf("root write must not create folders: %s", r.Method)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			f := &companyFiles{cfg: &config.Config{OcisURL: srv.URL}, http: srv.Client(), writeSpace: "write", readSpaces: map[string]bool{"write": true}}
			_, result, err := f.writeText(context.Background(), nil, companyText{"write", "report.md", "replacement"})
			if err != nil || result.Path != "report.md" || stored != "replacement" {
				t.Fatal("replacement response rejected", result, err)
			}
		})
	}
}

func TestCompanyFolderFileConflicts(t *testing.T) {
	f, files, _ := fixture(t)
	(*files)["/dav/spaces/write/file"] = []byte("preserve")
	(*files)["/dav/spaces/write/folder"] = nil
	for _, destination := range []string{"file/report.md", "file/nested/report.md", "folder"} {
		if _, _, err := f.writeText(context.Background(), nil, companyText{"write", destination, "replacement"}); err == nil {
			t.Fatalf("accepted folder/file conflict at %q", destination)
		}
	}
	if len(*files) != 3 || string((*files)["/dav/spaces/write/file"]) != "preserve" || (*files)["/dav/spaces/write/folder"] != nil {
		t.Fatal("conflict changed remote content")
	}
}

func TestCompanyWritesRejectScopePathsAndMethodsBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	f := &companyFiles{cfg: &config.Config{OcisURL: srv.URL}, http: srv.Client(), writeSpace: "write", readSpaces: map[string]bool{"write": true, "canonical": true}, gate: make(chan struct{}, 1)}
	f.remote = func(context.Context, string, string, io.Reader, io.Writer) error {
		requests.Add(1)
		return fmt.Errorf("unexpected staging access")
	}
	ctx := context.Background()
	paths := []companyPath{{"canonical", "report.md"}, {"other-agent", "report.md"}}
	for _, path := range []string{"", "/", "/x", "../x", "x/../../y", "x/./y", "x/%2e%2e/y", "x/%252e%252e/y", "x//y", "x/", "x\\y", "x\x00y", "x\ny"} {
		paths = append(paths, companyPath{"write", path})
	}
	for _, p := range paths {
		if _, _, err := f.writeText(ctx, nil, companyText{p.SpaceID, p.Path, "text"}); err == nil {
			t.Fatalf("text accepted invalid destination %+v", p)
		}
		if _, _, err := f.publish(ctx, nil, companyExport{p.SpaceID, p.Path, strings.Repeat("a", 32), strings.Repeat("b", 64)}); err == nil {
			t.Fatalf("publish accepted invalid destination %+v", p)
		}
	}
	for _, method := range []string{"DELETE", "MOVE", "COPY", "POST", "PATCH", "PROPPATCH", "LOCK", "UNLOCK", "HEAD", "put", ""} {
		if _, err := f.request(ctx, method, companyPath{"write", "report.md"}, nil, 0); err == nil {
			t.Fatalf("accepted unsupported method %q", method)
		}
	}
	for _, method := range []string{"PUT", "MKCOL", "PROPFIND"} {
		if _, err := f.request(ctx, method, companyPath{"canonical", "folder"}, nil, 0); err == nil {
			t.Fatalf("accepted %s outside output Space", method)
		}
	}
	f.writeSpace = ""
	if _, _, err := f.writeText(ctx, nil, companyText{"write", "report.md", "text"}); err == nil {
		t.Fatal("write accepted without configured output Space")
	}
	if requests.Load() != 0 {
		t.Fatal("denied operations reached DAV or sandbox")
	}
}

func TestCompanyExistingParentRequiresVerifiedCollection(t *testing.T) {
	valid := `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/spaces/write/reports/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
	for name, metadata := range map[string]string{
		"not-xml":         "broken",
		"oversize":        strings.Repeat("x", 65537),
		"file":            strings.Replace(valid, "<d:collection/>", "", 1),
		"denied-property": strings.Replace(valid, "200 OK", "403 Forbidden", 1),
		"wrong-parent":    strings.Replace(valid, "write/reports/", "write/elsewhere/", 1),
		"wrong-space":     strings.Replace(valid, "write/reports/", "canonical/reports/", 1),
		"wrong-origin":    strings.Replace(valid, "/dav/spaces/", "https://elsewhere.invalid/dav/spaces/", 1),
		"wrong-namespace": strings.Replace(valid, "DAV:", "not-DAV:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			var puts atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "MKCOL":
					w.WriteHeader(405)
				case "PROPFIND":
					w.WriteHeader(207)
					io.WriteString(w, metadata)
				default:
					puts.Add(1)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			f := &companyFiles{cfg: &config.Config{OcisURL: srv.URL}, http: srv.Client(), writeSpace: "write", readSpaces: map[string]bool{"write": true}}
			_, _, err := f.writeText(context.Background(), nil, companyText{"write", "reports/report.md", "text"})
			if err == nil || puts.Load() != 0 || !strings.Contains(err.Error(), `at "reports/report.md"`) {
				t.Fatal("unverified parent allowed upload or lost destination", err)
			}
		})
	}
}
