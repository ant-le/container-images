package tools

// Company profile for the pinned upstream oCIS MCP server. No OpenClaw or oCIS
// backend patch: discovery reuses upstream handlers; transfers stage outside
// OpenShell's mirrored roots. Credentials never enter the sandbox.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/owncloud/ocis-mcp-server/internal/client"
	"github.com/owncloud/ocis-mcp-server/internal/config"
)

//go:embed staging.py
var stagingScript string

const transferLimit int64 = 32 * 1024 * 1024
const textLimit int64 = 100 * 1024
const stagingRoot = "/sandbox/.ocis-transfer/"

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
var transferID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type companyFiles struct {
	cfg        *config.Config
	http       *http.Client
	writeSpace string
	readSpaces map[string]bool
	remote     func(context.Context, string, string, io.Reader, io.Writer) error
	gate       chan struct{}
}

// Profiles come from administrator-owned MCP commands, never tool arguments.
func ConfigureCompany() error {
	if dir := os.Getenv("OCIS_MCP_CREDENTIALS_DIR"); dir != "" {
		for _, key := range []string{"OCIS_MCP_APP_TOKEN_USER", "OCIS_MCP_APP_TOKEN_VALUE"} {
			file, err := os.Open(filepath.Join(dir, key))
			if os.IsNotExist(err) && key != "OCIS_MCP_APP_TOKEN_USER" && key != "OCIS_MCP_APP_TOKEN_VALUE" {
				os.Unsetenv(key)
				continue
			}
			if err != nil {
				return fmt.Errorf("cannot read company credential field %s", key)
			}
			data, err := io.ReadAll(io.LimitReader(file, 8193))
			file.Close()
			if err != nil || len(data) > 8192 {
				return fmt.Errorf("invalid company credential field %s", key)
			}
			if err := os.Setenv(key, strings.TrimSpace(string(data))); err != nil {
				return err
			}
		}
	}
	if err := configureScope(); err != nil {
		return err
	}
	if len(os.Args) == 1 {
		return nil
	}
	if len(os.Args) != 2 {
		return errors.New("expected one company profile")
	}
	profile := os.Args[1]
	if profile != "catalog" && profile != "transfer" {
		return errors.New("unknown company profile")
	}
	return os.Setenv("OCIS_MCP_PROFILE", profile)
}

type companyPath struct {
	SpaceID string `json:"space_id" jsonschema:"Space ID returned by ocis_list_my_spaces"`
	Path    string `json:"path" jsonschema:"Relative file path within the Space"`
}
type companyText struct {
	SpaceID string `json:"space_id"`
	Path    string `json:"path" jsonschema:"Requested outputs/<filename>; server allocates a fresh subfolder and returns the actual path"`
	Content string `json:"content" jsonschema:"UTF-8 text, at most 100 KiB"`
}
type companyExport struct {
	SpaceID    string `json:"space_id"`
	Path       string `json:"path" jsonschema:"Requested outputs/<filename>; server allocates a fresh subfolder and returns the actual path"`
	TransferID string `json:"transfer_id"`
	SHA256     string `json:"sha256" jsonschema:"SHA-256 calculated in the sandbox after closing the output file"`
}
type companyTicket struct {
	TransferID string `json:"transfer_id"`
}
type companyResult struct {
	TransferID string `json:"transfer_id,omitempty"`
	InputPath  string `json:"input_path,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
	Content    string `json:"content,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Path       string `json:"path,omitempty"`
}

func RegisterCompany(s *mcp.Server, c *client.Client, cfg *config.Config) error {
	u, err := url.Parse(cfg.OcisBaseURL())
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || cfg.TLSSkipVerify || cfg.Insecure {
		return errors.New("company profile requires a verified HTTPS origin")
	}
	if cfg.Transport != "stdio" || cfg.AuthMode != "app-token" || cfg.AppTokenUser == "" || cfg.AppTokenValue == "" {
		return errors.New("company profile requires stdio and an app token")
	}
	f := &companyFiles{cfg: cfg, writeSpace: os.Getenv("OCIS_MCP_WRITE_SPACE_ID"), gate: make(chan struct{}, 1)}
	f.readSpaces, err = parseReadSpaces(os.Getenv("OCIS_MCP_READ_SPACE_IDS"))
	if err != nil {
		return err
	}
	if f.writeSpace != "" && !f.readSpaces[f.writeSpace] {
		return errors.New("output Space must also be in read_space_ids for checksum verification")
	}
	f.http = cfg.NewHTTPClient()
	f.http.Timeout = 120 * time.Second
	f.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	profile := os.Getenv("OCIS_MCP_PROFILE")
	if profile == "catalog" {
		f.registerCatalog(s, c)
		mcp.AddTool(s, &mcp.Tool{Name: "ocis_read_text", Description: "Read UTF-8 text up to 100 KiB. Binary files require the sandbox transfer tools.", Annotations: readOnlyAnnotations()}, f.readText)
		if f.writeSpace != "" {
			mcp.AddTool(s, &mcp.Tool{Name: "ocis_create_text", Description: "Create a new UTF-8 document under this agent's configured output Space/outputs. Never overwrites.", Annotations: mutatingAnnotations()}, f.createText)
		}
		return nil
	}
	if profile != "transfer" {
		return errors.New("OCIS_MCP_PROFILE must be catalog or transfer")
	}
	gateway, workspace, sandbox := os.Getenv("OCIS_MCP_GATEWAY"), os.Getenv("OCIS_MCP_WORKSPACE"), os.Getenv("OCIS_MCP_SANDBOX")
	for _, v := range []string{gateway, workspace, sandbox} {
		if !safeName.MatchString(v) {
			return errors.New("transfer profile requires fixed gateway, workspace and sandbox names")
		}
	}
	f.remote = sshStaging(gateway, workspace, sandbox)
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_stage_file", Description: "Download up to 32 MiB into this agent's sandbox staging area, outside mirrored workspaces. Returns paths and SHA-256, never binary content.", Annotations: mutatingAnnotations()}, f.stageFile)
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_prepare_output", Description: "Reserve a staging output path for a generated file. Write and close it in sandbox exec, calculate SHA-256, then publish.", Annotations: mutatingAnnotations()}, f.prepareOutput)
	mcp.AddTool(s, &mcp.Tool{Name: "ocis_release_transfer", Description: "Remove this transfer's temporary input/output from the sandbox after use. Does not delete any oCIS file.", Annotations: mutatingAnnotations()}, f.release)
	if f.writeSpace != "" {
		mcp.AddTool(s, &mcp.Tool{Name: "ocis_publish_output", Description: "Snapshot and checksum a staged output, then create a new file under this agent's output Space/outputs. Never overwrites. Uses this agent's fixed sandbox.", Annotations: mutatingAnnotations()}, f.publish)
	}
	return nil
}

func escapedDAV(space, path string) (string, error) {
	if space == "" || strings.ContainsAny(space, "/\\%?#\x00\r\n") {
		return "", errors.New("invalid space ID")
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\%\x00\r\n") {
			return "", errors.New("invalid relative path")
		}
		parts[i] = url.PathEscape(part)
	}
	return "/dav/spaces/" + url.PathEscape(space) + "/" + strings.Join(parts, "/"), nil
}

func (f *companyFiles) request(ctx context.Context, method string, p companyPath, body io.Reader, size int64) (*http.Response, error) {
	if !f.readSpaces[p.SpaceID] {
		return nil, errors.New("Space is outside this agent's configured scope")
	}
	path, err := escapedDAV(p.SpaceID, p.Path)
	if err != nil {
		return nil, err
	}
	if method != "GET" && (f.writeSpace == "" || p.SpaceID != f.writeSpace || !strings.HasPrefix(p.Path, "outputs/")) {
		return nil, errors.New("writes are restricted to this agent's output Space/outputs")
	}
	req, err := http.NewRequestWithContext(ctx, method, f.cfg.OcisBaseURL()+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(f.cfg.AppTokenUser, f.cfg.AppTokenValue)
	if method == "PUT" {
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("If-None-Match", "*")
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, errors.New("oCIS request failed; writes may need reconciliation before retry")
	}
	if (method == "GET" && resp.StatusCode != 200) || (method != "GET" && resp.StatusCode != 201) {
		resp.Body.Close()
		return nil, fmt.Errorf("oCIS HTTP %d; no successful transfer confirmed", resp.StatusCode)
	}
	return resp, nil
}

func (f *companyFiles) readText(ctx context.Context, _ *mcp.CallToolRequest, in companyPath) (*mcp.CallToolResult, companyResult, error) {
	resp, err := f.request(ctx, "GET", in, nil, 0)
	if err != nil {
		return nil, companyResult{}, err
	}
	defer resp.Body.Close()
	if !isTextContentType(resp.Header.Get("Content-Type")) {
		return nil, companyResult{}, errors.New("not a text document; use sandbox transfer")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, textLimit+1))
	if err != nil {
		return nil, companyResult{}, err
	}
	truncated := int64(len(b)) > textLimit
	if truncated {
		b = b[:textLimit]
		for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
	}
	if !utf8.Valid(b) {
		return nil, companyResult{}, errors.New("not UTF-8")
	}
	return nil, companyResult{Content: string(b), Size: int64(len(b)), Truncated: truncated}, nil
}

func (f *companyFiles) createText(ctx context.Context, _ *mcp.CallToolRequest, in companyText) (*mcp.CallToolResult, companyResult, error) {
	if int64(len(in.Content)) > textLimit || !utf8.ValidString(in.Content) {
		return nil, companyResult{}, errors.New("text exceeds limit or is not UTF-8")
	}
	return f.putVerified(ctx, companyPath{in.SpaceID, in.Path}, bytes.NewReader([]byte(in.Content)))
}

func (f *companyFiles) putVerified(ctx context.Context, p companyPath, file io.ReadSeeker) (call *mcp.CallToolResult, result companyResult, err error) {
	// oCIS 8.2.1 ignores If-None-Match on PUT. Never PUT a model-selected
	// existing path: exclusively reserve a fresh, random collection first.
	// MKCOL rejects existing collections. Failed uploads leave a collection
	// for human reconciliation; never reuse it or delete remote content here.
	if _, err := escapedDAV(p.SpaceID, p.Path); err != nil {
		return nil, companyResult{}, err
	}
	parts := strings.Split(p.Path, "/")
	if len(parts) != 2 || parts[0] != "outputs" {
		return nil, companyResult{}, errors.New("choose outputs/<filename>; a unique output folder is allocated automatically")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, transferLimit+1))
	if err != nil || n > transferLimit {
		return nil, companyResult{}, errors.New("invalid export size")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, companyResult{}, err
	}
	collection := "outputs/" + newTicket().TransferID
	destination := collection + "/" + parts[1]
	defer func() {
		if err != nil {
			err = fmt.Errorf("publication in Space %q at %q not confirmed; inspect this destination before retry: %w", p.SpaceID, destination, err)
		}
	}()
	reserved, err := f.request(ctx, "MKCOL", companyPath{p.SpaceID, collection}, nil, 0)
	if err != nil {
		return nil, companyResult{}, err
	}
	reserved.Body.Close()
	p.Path = destination
	resp, err := f.request(ctx, "PUT", p, file, n)
	if err != nil {
		return nil, companyResult{}, err
	}
	resp.Body.Close()
	// Verify actual remote bytes. ETags are opaque, not content hashes.
	resp, err = f.request(ctx, "GET", p, nil, 0)
	if err != nil {
		return nil, companyResult{}, errors.New("created file but read-back verification failed; inspect before retry")
	}
	defer resp.Body.Close()
	check := sha256.New()
	got, err := io.Copy(check, io.LimitReader(resp.Body, transferLimit+1))
	if err != nil || got != n || !bytes.Equal(check.Sum(nil), h.Sum(nil)) {
		return nil, companyResult{}, errors.New("created file but checksum verification failed; inspect before retry")
	}
	return nil, companyResult{Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Path: p.Path}, nil
}

func newTicket() companyResult {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	name := hex.EncodeToString(id)
	return companyResult{TransferID: name, OutputPath: stagingRoot + name + ".output"}
}

func (f *companyFiles) withTransfer(ctx context.Context, fn func(context.Context) (companyResult, error)) (*mcp.CallToolResult, companyResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	select {
	case f.gate <- struct{}{}:
		defer func() { <-f.gate }()
	case <-ctx.Done():
		return nil, companyResult{}, ctx.Err()
	}
	result, err := fn(ctx)
	return nil, result, err
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > b.remaining {
		return 0, errors.New("transfer exceeds size limit")
	}
	n, err := b.w.Write(p)
	b.remaining -= int64(n)
	return n, err
}

func (f *companyFiles) stageFile(ctx context.Context, _ *mcp.CallToolRequest, in companyPath) (*mcp.CallToolResult, companyResult, error) {
	return f.withTransfer(ctx, func(ctx context.Context) (companyResult, error) {
		resp, err := f.request(ctx, "GET", in, nil, 0)
		if err != nil {
			return companyResult{}, err
		}
		defer resp.Body.Close()
		if resp.ContentLength > transferLimit {
			return companyResult{}, errors.New("file exceeds 32 MiB")
		}
		result := newTicket()
		var reply bytes.Buffer
		h := sha256.New()
		err = f.remote(ctx, "put", result.TransferID, io.TeeReader(io.LimitReader(resp.Body, transferLimit+1), h), &boundedWriter{&reply, 1024})
		if err != nil {
			return companyResult{}, fmt.Errorf("staging failed for transfer %s; release it after checking no active transfer remains: %w", result.TransferID, err)
		}
		var check companyResult
		if json.Unmarshal(reply.Bytes(), &check) != nil || check.SHA256 != hex.EncodeToString(h.Sum(nil)) || check.Size > transferLimit {
			return companyResult{}, errors.New("staging checksum mismatch")
		}
		result.InputPath, result.Size, result.SHA256 = stagingRoot+result.TransferID+".input", check.Size, check.SHA256
		return result, nil
	})
}

func (f *companyFiles) prepareOutput(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, companyResult, error) {
	return f.withTransfer(ctx, func(ctx context.Context) (companyResult, error) {
		result := newTicket()
		return result, f.remote(ctx, "reserve", result.TransferID, nil, &boundedWriter{io.Discard, 1024})
	})
}

func (f *companyFiles) release(ctx context.Context, _ *mcp.CallToolRequest, in companyTicket) (*mcp.CallToolResult, companyResult, error) {
	return f.withTransfer(ctx, func(ctx context.Context) (companyResult, error) {
		if !transferID.MatchString(in.TransferID) {
			return companyResult{}, errors.New("invalid transfer ID")
		}
		return companyResult{TransferID: in.TransferID}, f.remote(ctx, "release", in.TransferID, nil, &boundedWriter{io.Discard, 1024})
	})
}

func (f *companyFiles) publish(ctx context.Context, _ *mcp.CallToolRequest, in companyExport) (*mcp.CallToolResult, companyResult, error) {
	return f.withTransfer(ctx, func(ctx context.Context) (companyResult, error) {
		if !transferID.MatchString(in.TransferID) || !shaPattern.MatchString(in.SHA256) {
			return companyResult{}, errors.New("invalid transfer ID or SHA-256")
		}
		file, err := os.CreateTemp("", "ocis-export-*")
		if err != nil {
			return companyResult{}, err
		}
		defer os.Remove(file.Name())
		defer file.Close()
		h := sha256.New()
		err = f.remote(ctx, "get", in.TransferID, nil, &boundedWriter{io.MultiWriter(file, h), transferLimit})
		if err != nil {
			return companyResult{}, err
		}
		if hex.EncodeToString(h.Sum(nil)) != in.SHA256 {
			return companyResult{}, errors.New("export checksum mismatch; nothing uploaded")
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return companyResult{}, err
		}
		_, result, err := f.putVerified(ctx, companyPath{in.SpaceID, in.Path}, file)
		return result, err
	})
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func sshStaging(gateway, workspace, sandbox string) func(context.Context, string, string, io.Reader, io.Writer) error {
	return func(ctx context.Context, op, id string, input io.Reader, output io.Writer) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(home, ".config/openclaw/openshell-maintenance")); !os.IsNotExist(err) {
			return errors.New("sandbox maintenance active or status unreadable")
		}
		proxy := "/usr/local/bin/openshell ssh-proxy --gateway-name " + gateway + " --workspace " + workspace + " --name " + sandbox
		command := "/usr/local/bin/python3 -I -S -c " + shellQuote(stagingScript) + " " + shellQuote(op) + " " + shellQuote(id)
		cmd := exec.CommandContext(ctx, "/usr/bin/ssh", "-F", "/dev/null", "-T", "-l", "sandbox",
			"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
			"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "GlobalKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR", "-o", "SetEnv=OPENSHELL_NO_LOGIN_SHELL=1", "-o", "ProxyCommand="+proxy, "sandbox", command)
		cmd.Stdin, cmd.Stdout = input, output
		// No inherited model/oCIS credentials in subprocess environments.
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin"}
		cmd.WaitDelay = 5 * time.Second
		if err := cmd.Run(); err != nil {
			return errors.New("sandbox staging failed; check readiness, maintenance and staging budget")
		}
		return nil
	}
}
