package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owncloud/ocis-mcp-server/internal/client"
	"github.com/owncloud/ocis-mcp-server/internal/config"
)

func fixture(t *testing.T) (*companyFiles, *map[string][]byte, string) {
	t.Helper()
	files := map[string][]byte{"/dav/spaces/read/binary.pdf": bytes.Repeat([]byte{0, 255, 42, 4}, 400000)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "company-agent" || token != "test-token" {
			w.WriteHeader(401)
			return
		}
		if strings.Contains(r.URL.Path, "/private/") {
			w.WriteHeader(403)
			return
		}
		if r.Method == "MKCOL" {
			if _, exists := files[r.URL.Path]; exists {
				w.WriteHeader(405)
				return
			}
			if parent := path.Dir(r.URL.Path); parent != "/dav/spaces/write" {
				if b, ok := files[parent]; !ok || b != nil {
					w.WriteHeader(409)
					return
				}
			}
			files[r.URL.Path] = nil
			w.WriteHeader(201)
			return
		}
		if r.Method == "PUT" {
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Match") != "" {
				t.Error("unsupported conditional-write guarantee")
			}
			old, existed := files[r.URL.Path]
			if existed && old == nil {
				w.WriteHeader(409)
				return
			}
			if parent := path.Dir(r.URL.Path); parent != "/dav/spaces/write" {
				if b, ok := files[parent]; !ok || b != nil {
					w.WriteHeader(409)
					return
				}
			}
			b, _ := io.ReadAll(r.Body)
			files[r.URL.Path] = b
			if existed {
				w.WriteHeader(204)
				return
			}
			w.WriteHeader(201)
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.Method == "PROPFIND" {
			if r.Header.Get("Depth") != "0" {
				t.Error("parent lookup must be depth zero")
			}
			collection := ""
			if b == nil {
				collection = "<d:collection/>"
			}
			var href bytes.Buffer
			xml.EscapeText(&href, []byte(r.URL.EscapedPath()))
			w.WriteHeader(207)
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype>%s</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href.String(), collection)
			return
		}
		if r.Method != "GET" {
			t.Errorf("unexpected DAV method %s", r.Method)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	}))
	t.Cleanup(server.Close)
	root := filepath.Join(t.TempDir(), "staging")
	f := &companyFiles{cfg: &config.Config{OcisURL: server.URL, AppTokenUser: "company-agent", AppTokenValue: "test-token"}, http: server.Client(), writeSpace: "write", readSpaces: map[string]bool{"read": true, "write": true, "private": true}, gate: make(chan struct{}, 1)}
	f.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	f.remote = func(ctx context.Context, op, id string, input io.Reader, output io.Writer) error {
		// Same helper and streams as SSH; only the authenticated transport is replaced.
		code := strings.Replace(stagingScript, `ROOT = "/sandbox/.ocis-transfer"`, fmt.Sprintf("ROOT = %q", root), 1)
		cmd := exec.CommandContext(ctx, "python3", "-I", "-S", "-c", code, op, id)
		cmd.Stdin, cmd.Stdout = input, output
		return cmd.Run()
	}
	return f, &files, root
}

func TestCompanyBinaryRoundTrip(t *testing.T) {
	f, files, root := fixture(t)
	ctx := context.Background()
	_, staged, err := f.stageFile(ctx, nil, companyPath{"read", "binary.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	want := (*files)["/dav/spaces/read/binary.pdf"]
	if staged.Size != int64(len(want)) || staged.SHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) {
		t.Fatal("binary truncated or corrupted")
	}
	// Simulate the completed sandbox output, then exercise the REAL get helper.
	if err := os.WriteFile(filepath.Join(root, staged.TransferID+".output"), want, 0600); err != nil {
		t.Fatal(err)
	}
	_, published, err := f.publish(ctx, nil, companyExport{"write", "report.pdf", staged.TransferID, staged.SHA256})
	if err != nil || published.SHA256 != staged.SHA256 || published.Path != "report.pdf" {
		t.Fatalf("publish: %v", err)
	}
	if !bytes.Equal((*files)["/dav/spaces/write/"+published.Path], want) {
		t.Fatal("remote bytes differ")
	}
	updated := []byte("updated binary\x00\xff")
	if err := os.WriteFile(filepath.Join(root, staged.TransferID+".output"), updated, 0600); err != nil {
		t.Fatal(err)
	}
	updatedHash := fmt.Sprintf("%x", sha256.Sum256(updated))
	for _, destination := range []string{"report.pdf", "reports/2026/report.pdf", "reports/2026/report.pdf"} {
		_, result, err := f.publish(ctx, nil, companyExport{"write", destination, staged.TransferID, updatedHash})
		if err != nil || result.Path != destination || result.SHA256 != updatedHash || !bytes.Equal((*files)["/dav/spaces/write/"+destination], updated) {
			t.Fatal("exact-path create/replace failed", result, err)
		}
	}
	if _, _, err = f.publish(ctx, nil, companyExport{"write", "outputs/wrong.pdf", staged.TransferID, strings.Repeat("0", 64)}); err == nil {
		t.Fatal("wrong hash accepted")
	}
	if _, _, err = f.publish(ctx, nil, companyExport{"read", "outputs/forbidden.pdf", staged.TransferID, staged.SHA256}); err == nil {
		t.Fatal("wrong space accepted")
	}
	if _, _, err = f.stageFile(ctx, nil, companyPath{"private", "hidden.pdf"}); err == nil {
		t.Fatal("permission denial ignored")
	}
	if _, _, err = f.release(ctx, nil, companyTicket{staged.TransferID}); err != nil {
		t.Fatal(err)
	}
}

func TestCompanyPathsAndLimits(t *testing.T) {
	for _, p := range []string{"../x", "outputs/../x", "/x", "outputs/%2e%2e/x", "outputs//x", "x\\y", "x\x00y"} {
		if _, err := escapedDAV("write", p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	got, err := escapedDAV("id$space", "outputs/Agenda #1?.md")
	if err != nil || !strings.Contains(got, "Agenda%20%231%3F.md") {
		t.Fatal(got, err)
	}
	f, _, _ := fixture(t)
	if _, _, err := f.writeText(context.Background(), nil, companyText{"write", "a.md", strings.Repeat("x", int(textLimit+1))}); err == nil {
		t.Fatal("oversize text accepted")
	}
	b := boundedWriter{io.Discard, 3}
	if _, err := b.Write([]byte("1234")); err == nil {
		t.Fatal("oversize binary accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.gate <- struct{}{}
	if _, _, err := f.prepareOutput(ctx, nil, struct{}{}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestCompanyToolSurface(t *testing.T) {
	t.Setenv("OCIS_MCP_ALLOW_DELETE", "false")
	t.Setenv("OCIS_MCP_PROFILE", "catalog")
	t.Setenv("OCIS_MCP_WRITE_SPACE_ID", "write")
	t.Setenv("OCIS_MCP_READ_SPACE_IDS", `["read","write"]`)
	cfg := &config.Config{OcisURL: "https://example.invalid", Transport: "stdio", AuthMode: "app-token", AppTokenUser: "agent", AppTokenValue: "test", HTTPTimeout: time.Second}
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
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
	cc := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := cc.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 6 {
		t.Fatalf("unexpected tools: %v", list.Tools)
	}
	foundWrite := false
	for _, tool := range list.Tools {
		if tool.Name == "ocis_write_text" {
			foundWrite = true
			if !strings.Contains(tool.Description, "replace") || !strings.Contains(tool.Description, "last-writer-wins") {
				t.Fatal("write contract omits replacement or concurrency semantics")
			}
		}
		if strings.Contains(tool.Name, "delete") || tool.Name == "ocis_upload_file" || tool.Name == "ocis_download_file" || tool.Name == "ocis_create_text" {
			t.Fatal("unsafe upstream tool registered")
		}
	}
	if !foundWrite {
		t.Fatal("missing ocis_write_text")
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ocis_delete_file", Arguments: map[string]any{}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatal("hidden mutation callable")
	}
	cfg.Insecure = true
	if RegisterCompany(s, client.New(cfg), cfg) == nil {
		t.Fatal("insecure configuration accepted")
	}
}

func TestCompanyProfileAndCredentialFiles(t *testing.T) {
	before := os.Args
	t.Cleanup(func() { os.Args = before })
	dir := t.TempDir()
	t.Setenv("OCIS_MCP_CREDENTIALS_DIR", dir)
	for _, key := range []string{"OCIS_MCP_APP_TOKEN_USER", "OCIS_MCP_APP_TOKEN_VALUE", "OCIS_MCP_SANDBOX_MAIN", "OCIS_MCP_SANDBOX_TEAM", "OCIS_MCP_SANDBOX_STOCKS", "OCIS_MCP_WRITE_SPACE_ID", "OCIS_MCP_PROFILE", "OCIS_MCP_SANDBOX"} {
		t.Setenv(key, "")
	}
	os.Args = []string{"ocis-company-mcp", "transfer"}
	if ConfigureCompany() == nil {
		t.Fatal("missing credentials accepted")
	}
	for key, value := range map[string]string{"OCIS_MCP_APP_TOKEN_USER": "agent", "OCIS_MCP_APP_TOKEN_VALUE": "test-token"} {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OCIS_MCP_SANDBOX", "oc-main")
	t.Setenv("OCIS_MCP_READ_SPACE_IDS", `["read"]`)
	if err := ConfigureCompany(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OCIS_MCP_SANDBOX") != "oc-main" || os.Getenv("OCIS_MCP_PROFILE") != "transfer" {
		t.Fatal("wrong sandbox profile")
	}
	t.Setenv("OCIS_MCP_SANDBOX", "oc-team")
	if err := ConfigureCompany(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OCIS_MCP_SANDBOX") != "oc-team" {
		t.Fatal("cross-agent binding")
	}
	t.Setenv("OCIS_MCP_GATEWAY", "company-shell")
	t.Setenv("OCIS_MCP_WORKSPACE", "default")
	cfg := &config.Config{OcisURL: "https://example.invalid", Transport: "stdio", AuthMode: "app-token", AppTokenUser: "agent", AppTokenValue: "test"}
	s := mcp.NewServer(&mcp.Implementation{Name: "transfer", Version: "1"}, nil)
	if err := RegisterCompany(s, client.New(cfg), cfg); err != nil {
		t.Fatal(err)
	}
	// Missing mapped sandbox is an error, never a "last-used sandbox" fallback.
	t.Setenv("OCIS_MCP_SANDBOX", "")
	if err := ConfigureCompany(); err != nil {
		t.Fatal(err)
	}
	if RegisterCompany(s, client.New(cfg), cfg) == nil {
		t.Fatal("missing sandbox binding accepted")
	}
}

func TestCompanyDownloadInterruptions(t *testing.T) {
	for _, mode := range []string{"short", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := fixture(t)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "10000")
				w.Write([]byte("partial"))
				w.(http.Flusher).Flush()
				if mode == "cancel" {
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			f.cfg.OcisURL = srv.URL
			f.http = srv.Client()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, _, err := f.stageFile(ctx, nil, companyPath{"read", "broken.pdf"}); err == nil {
				t.Fatal("interrupted stream reported success")
			}
		})
	}
}

func TestCompanyInternalRoutePreservesTLSIdentity(t *testing.T) {
	var host, sni string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, sni = r.Host, r.TLS.ServerName
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://elsewhere.invalid/", 302)
			return
		}
		w.Write([]byte("internal"))
	}))
	defer srv.Close()
	internal, _ := url.Parse(srv.URL)
	publicName := srv.Certificate().DNSNames[0]
	t.Setenv("OCIS_MCP_CONNECT_ADDRESS", internal.Host)
	t.Setenv("HTTPS_PROXY", "http://must-not-use.invalid:9999")
	cfg := &config.Config{OcisURL: "https://" + publicName, HTTPTimeout: time.Second}
	c := cfg.NewHTTPClient()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	resp, err := c.Get(cfg.OcisURL + "/check")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if host != publicName || sni != publicName {
		t.Fatal("canonical Host/SNI lost", host, sni)
	}
	resp, err = c.Get(cfg.OcisURL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatal("followed redirect")
	}
	// The TCP override must not disable certificate validation.
	c.Transport.(*http.Transport).CloseIdleConnections()
	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}
	if _, err = c.Get(cfg.OcisURL + "/check"); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func TestCompanyAmbiguousPublishIdentifiesDestination(t *testing.T) {
	for _, mode := range []string{"lost-put-response", "read-back-denied", "read-back-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var storedPath string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "MKCOL":
					w.WriteHeader(201)
				case "PUT":
					io.Copy(io.Discard, r.Body)
					storedPath = r.URL.Path
					if mode == "lost-put-response" {
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						return
					}
					w.WriteHeader(201)
				case "GET":
					if mode == "read-back-mismatch" {
						w.Write([]byte("racing")) // Same length, different checksum.
					} else {
						w.WriteHeader(403)
					}
				}
			}))
			defer srv.Close()
			f := &companyFiles{cfg: &config.Config{OcisURL: srv.URL}, http: srv.Client(), writeSpace: "write", readSpaces: map[string]bool{"write": true}}
			_, _, err := f.putVerified(context.Background(), companyPath{"write", "reports/result.md"}, strings.NewReader("result"))
			if err == nil || storedPath != "/dav/spaces/write/reports/result.md" || !strings.Contains(err.Error(), `at "reports/result.md"`) || !strings.Contains(err.Error(), `Space "write"`) {
				t.Fatal("lost reconciliation target", err)
			}
		})
	}
}
