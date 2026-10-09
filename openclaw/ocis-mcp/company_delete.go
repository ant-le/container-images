package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var fullFileID = regexp.MustCompile(`^[a-zA-Z0-9_-]+\$[a-zA-Z0-9_-]+![a-zA-Z0-9_-]+$`)

type companyDelete struct {
	SpaceID string `json:"space_id"`
	Path    string `json:"path" jsonschema:"Exact nonempty relative file path in your own output Space; never a folder or Space root"`
	FileID  string `json:"file_id" jsonschema:"Full file_id from current ocis_get_file_info or ocis_list_files metadata; must match this path and owning Space"`
}

type companyDeleteResult struct {
	Path   string `json:"path"`
	FileID string `json:"file_id"`
	Status string `json:"status" jsonschema:"trashed or already_absent; not a permanent purge or quota-reclamation guarantee"`
}

// DELETE stays unsupported by the general byte-transfer request function.
// Only this opt-in handler can issue it, after scope, identity and type checks.
func (f *companyFiles) deleteFile(ctx context.Context, _ *mcp.CallToolRequest, in companyDelete) (_ *mcp.CallToolResult, out companyDeleteResult, err error) {
	if !f.allowDelete {
		return nil, out, errors.New("file deletion is not enabled for this agent")
	}
	p := companyPath{in.SpaceID, in.Path}
	davPath, err := f.authorizeRequest("PROPFIND", p)
	if err != nil {
		return nil, out, err
	}
	_, spaceNode, ok := strings.Cut(in.SpaceID, "$")
	if !ok || !fullFileID.MatchString(in.FileID) || !strings.HasPrefix(in.FileID, in.SpaceID+"!") || in.FileID == in.SpaceID+"!"+spaceNode {
		return nil, out, errors.New("file_id must identify a non-root resource in this agent's output Space")
	}
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	idPath := "/dav/spaces/" + url.PathEscape(in.FileID)
	absent, err := f.inspectDeletePath(ctx, davPath, p, in.FileID)
	if err != nil {
		return nil, out, err
	}
	if absent {
		// A missing pathname alone is not an idempotent success: the same ID
		// might have been moved elsewhere. Never follow it to delete that file.
		status, _, err := f.deleteHTTP(ctx, "PROPFIND", idPath)
		if err != nil || status != http.StatusNotFound {
			return nil, out, errors.New("requested path is absent but file-ID absence is not confirmed; inspect before retry")
		}
		return nil, companyDeleteResult{in.Path, in.FileID, "already_absent"}, nil
	}
	// DAV's empty resourcetype is also used for non-file resources. Require
	// Graph's file facet, which oCIS emits only for RESOURCE_TYPE_FILE.
	graphPath := "/graph/v1.0/drives/" + url.PathEscape(in.SpaceID) + "/items/" + url.PathEscape(in.FileID)
	status, data, err := f.deleteHTTP(ctx, "GET", graphPath)
	var item struct {
		ID   string `json:"id"`
		File *struct {
			MimeType string `json:"mimeType"`
		} `json:"file"`
		Folder          *struct{}       `json:"folder"`
		RemoteItem      json.RawMessage `json:"remoteItem"`
		ParentReference struct {
			DriveID string `json:"driveId"`
		} `json:"parentReference"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &item) != nil || item.ID != in.FileID || item.File == nil || item.File.MimeType == "" || item.Folder != nil || (len(item.RemoteItem) != 0 && string(item.RemoteItem) != "null") || item.ParentReference.DriveID != in.SpaceID {
		return nil, out, errors.New("cannot verify an ordinary file in the owning output Space; nothing deleted")
	}
	// Reduce stale-path/move windows before mutation. oCIS 8.2.1 does not
	// enforce If-Match on DELETE; this is not an atomic compare-and-swap.
	absent, err = f.inspectDeletePath(ctx, davPath, p, in.FileID)
	if err != nil || absent {
		return nil, out, errors.New("file path changed before deletion; inspect before retry; nothing deleted")
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("deletion in Space %q at %q for file_id %q not confirmed; inspect this path and ID before retry: %w", in.SpaceID, in.Path, in.FileID, err)
		}
	}()
	// One encoded resource-ID segment, no appended path. Normal oCIS deletion
	// moves this node to the Space trash. No permanent-delete API is exposed.
	status, _, err = f.deleteHTTP(ctx, "DELETE", idPath)
	if err != nil || status != http.StatusNoContent {
		return nil, out, fmt.Errorf("DELETE did not confirm success (HTTP %d)", status)
	}
	status, _, err = f.deleteHTTP(ctx, "PROPFIND", idPath)
	if err != nil || status != http.StatusNotFound {
		return nil, out, errors.New("DELETE accepted but file-ID absence could not be verified")
	}
	return nil, companyDeleteResult{in.Path, in.FileID, "trashed"}, nil
}

func (f *companyFiles) inspectDeletePath(ctx context.Context, endpoint string, p companyPath, expectedID string) (bool, error) {
	status, data, err := f.deleteHTTP(ctx, "PROPFIND", endpoint)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return true, nil
	}
	var ms struct {
		XMLName   xml.Name `xml:"DAV: multistatus"`
		Responses []struct {
			Href      string `xml:"DAV: href"`
			Propstats []struct {
				Status string `xml:"DAV: status"`
				Prop   struct {
					Type *struct {
						Inner string `xml:",innerxml"`
					} `xml:"DAV: resourcetype"`
					FileID string `xml:"http://owncloud.org/ns fileid"`
				} `xml:"DAV: prop"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	if status != http.StatusMultiStatus || xml.Unmarshal(data, &ms) != nil || len(ms.Responses) != 1 {
		return false, errors.New("cannot verify deletion target metadata; nothing deleted")
	}
	href, err := url.Parse(ms.Responses[0].Href)
	origin, _ := url.Parse(f.cfg.OcisBaseURL())
	if err != nil || href.RawQuery != "" || href.Fragment != "" || href.User != nil ||
		(href.IsAbs() && (href.Scheme != origin.Scheme || href.Host != origin.Host)) ||
		(!href.IsAbs() && href.Host != "") || href.Path != "/dav/spaces/"+p.SpaceID+"/"+p.Path {
		return false, errors.New("deletion metadata does not match the requested path; nothing deleted")
	}
	types, ids := 0, 0
	for _, ps := range ms.Responses[0].Propstats {
		status := strings.Fields(ps.Status)
		if len(status) < 2 || status[1] != "200" {
			continue
		}
		if ps.Prop.Type != nil {
			types++
			if strings.TrimSpace(ps.Prop.Type.Inner) != "" {
				return false, errors.New("folders and non-file resources cannot be deleted")
			}
		}
		if ps.Prop.FileID != "" {
			ids++
			if ps.Prop.FileID != expectedID {
				return false, errors.New("file_id is stale or does not match this path; nothing deleted")
			}
		}
	}
	if types != 1 || ids != 1 {
		return false, errors.New("file type/ID metadata missing or ambiguous; nothing deleted")
	}
	return false, nil
}

func (f *companyFiles) deleteHTTP(ctx context.Context, method, endpoint string) (int, []byte, error) {
	var body io.Reader
	if method == "PROPFIND" {
		body = strings.NewReader(`<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><d:resourcetype/><oc:fileid/></d:prop></d:propfind>`)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.cfg.OcisBaseURL()+endpoint, body)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(f.cfg.AppTokenUser, f.cfg.AppTokenValue)
	if method == "PROPFIND" {
		req.Header.Set("Depth", "0")
		req.Header.Set("Content-Type", "application/xml")
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return 0, nil, errors.New("oCIS deletion/inspection request failed")
	}
	defer resp.Body.Close()
	// Do not read or forward untrusted error bodies or unexpected file content.
	if (method == "PROPFIND" && resp.StatusCode == http.StatusMultiStatus) || (method == "GET" && resp.StatusCode == http.StatusOK) {
		data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
		if err != nil || len(data) > 65536 {
			return resp.StatusCode, nil, errors.New("invalid or oversized deletion metadata")
		}
		return resp.StatusCode, data, nil
	}
	return resp.StatusCode, nil, nil
}
