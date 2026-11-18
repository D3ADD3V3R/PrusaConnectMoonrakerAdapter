package moonraker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

func parseUploadBase(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/websocket") {
		return nil, fmt.Errorf("upload endpoint needs a /websocket URL without credentials, query or fragment")
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = strings.TrimSuffix(u.Path, "/websocket")
	u.RawPath = ""
	return u, nil
}

func (s *Session) prepareUploads(ctx context.Context, address string, caps connectorapi.Set) error {
	target, err := websocketURL(address)
	if err != nil {
		return err
	}
	u, err := parseUploadBase(target)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout:       5 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, u.JoinPath("server/files/roots").String(), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe upload root: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("probe upload root: HTTP %d", response.StatusCode)
	}
	var reply struct {
		Result []struct {
			Name        string `json:"name"`
			Permissions string `json:"permissions"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&reply); err != nil {
		return err
	}
	for _, root := range reply.Result {
		if root.Name == "gcodes" && strings.Contains(root.Permissions, "w") {
			caps[connectorapi.VerbFileUpload] = connectorapi.Value{Flag: true}
			s.probe[connectorapi.VerbFileUpload] = connectorapi.Value{Flag: true}
		}
	}
	s.uploads.client = client
	s.uploads.baseURL = u
	return nil
}
