package moonraker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

type uploadState struct {
	preparation sync.Mutex
	transfer    sync.Mutex
	mu          sync.Mutex
	closed      bool
	cancel      context.CancelFunc
	client      *http.Client
	baseURL     *url.URL
}

var _ connectorapi.Transfers = (*Session)(nil)

// Send uploads only to gcodes and verifies the stored bytes before reporting success.
func (s *Session) Send(ctx context.Context, upload connectorapi.Upload) error {
	if !s.caps.Has(connectorapi.VerbFileUpload) {
		return connectorapi.ErrNoTransfers
	}
	if !s.uploads.transfer.TryLock() {
		return connectorapi.ErrTransferBusy
	}
	defer s.uploads.transfer.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.uploads.mu.Lock()
	if s.uploads.closed {
		s.uploads.mu.Unlock()
		return connectorapi.ErrTransferFailed
	}
	s.uploads.cancel = cancel
	s.uploads.mu.Unlock()
	defer func() { s.uploads.mu.Lock(); s.uploads.cancel = nil; s.uploads.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	filename, err := destination(upload.Path)
	if err != nil {
		return err
	}
	if upload.Content == nil || upload.Content.Size() < 0 {
		return fmt.Errorf("%w: missing content or negative size", connectorapi.ErrTransferRefused)
	}
	size := upload.Content.Size()
	artifact, err := s.inspectUpload(ctx, upload)
	if err != nil {
		return fmt.Errorf("%w: %w", connectorapi.ErrTransferRefused, err)
	}
	source, err := upload.Content.Open(ctx, 0)
	if err != nil {
		return fmt.Errorf("open upload content: %w", err)
	}
	defer source.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = source.Close() })
	defer stopClose()
	body, err := os.CreateTemp("", "pcma-upload-*")
	if err != nil {
		return fmt.Errorf("create upload spool: %w", err)
	}
	defer os.Remove(body.Name())
	defer body.Close()
	form := multipart.NewWriter(body)
	for _, field := range []struct{ name, value string }{
		{"root", "gcodes"}, {"path", path.Dir(filename)}, {"print", "false"},
	} {
		if err := form.WriteField(field.name, field.value); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("file", path.Base(filename))
	if err != nil {
		return err
	}
	hash := sha256.New()
	// Read at most the declared length, then check for surplus bytes separately.
	written, err := io.Copy(io.MultiWriter(part, hash), io.LimitReader(&contextReader{ctx, source}, size))
	if err != nil {
		return fmt.Errorf("spool upload: %w", err)
	}
	var extra [1]byte
	extraCount, extraErr := io.ReadFull(&contextReader{ctx, source}, extra[:])
	if written != size || extraCount != 0 || !errors.Is(extraErr, io.EOF) {
		return fmt.Errorf("%w: content length differs from declared size", connectorapi.ErrTransferRefused)
	}
	if err := form.WriteField("checksum", fmt.Sprintf("%x", hash.Sum(nil))); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	info, err := body.Stat()
	if err != nil {
		return err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.uploads.baseURL.JoinPath("server/files/upload").String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.ContentLength = info.Size()
	if upload.Progress != nil {
		upload.Progress(connectorapi.Progress{Total: size, Ref: upload.Path})
	}
	response, err := s.uploads.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: upload: %w", connectorapi.ErrTransferFailed, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: upload HTTP %d", connectorapi.ErrTransferFailed, response.StatusCode)
	}
	var reply struct {
		Item struct {
			Root string `json:"root"`
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"item"`
		PrintStarted bool `json:"print_started"`
		PrintQueued  bool `json:"print_queued"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&reply); err != nil {
		return fmt.Errorf("%w: upload reply: %w", connectorapi.ErrTransferFailed, err)
	}
	item := reply.Item
	if item.Root != "gcodes" || item.Path != filename || item.Size != size || reply.PrintStarted || reply.PrintQueued {
		return fmt.Errorf("%w: unexpected destination or automatic print action", connectorapi.ErrTransferFailed)
	}
	if err := s.verify(ctx, filename, size, hash.Sum(nil)); err != nil {
		return err
	}
	if artifact != nil {
		if artifact.SHA256 != fmt.Sprintf("%x", hash.Sum(nil)) || artifact.Size != size {
			return fmt.Errorf("%w: artifact changed while uploading", connectorapi.ErrTransferFailed)
		}
		if err := s.executions.store.register(ctx, upload.Path, *artifact); err != nil {
			return fmt.Errorf("%w: save artifact receipt: %w", connectorapi.ErrTransferFailed, err)
		}
	}
	if upload.Progress != nil {
		upload.Progress(connectorapi.Progress{Total: size, Sent: size, Received: size, Known: true, Ref: upload.Path})
	}
	return nil
}

func (s *Session) verify(ctx context.Context, filename string, size int64, expected []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.uploads.baseURL.JoinPath("server/files/gcodes", filename).String(), nil)
	if err != nil {
		return err
	}
	response, err := s.uploads.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: verify: %w", connectorapi.ErrTransferFailed, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: verify HTTP %d", connectorapi.ErrTransferFailed, response.StatusCode)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(response.Body, size))
	if err != nil {
		return fmt.Errorf("%w: verify: %w", connectorapi.ErrTransferFailed, err)
	}
	var extra [1]byte
	n, tailErr := io.ReadFull(response.Body, extra[:])
	if written != size || n != 0 || !errors.Is(tailErr, io.EOF) || !bytes.Equal(hash.Sum(nil), expected) {
		return fmt.Errorf("%w: stored content differs from upload", connectorapi.ErrTransferFailed)
	}
	return nil
}

func destination(value string) (string, error) {
	relative, ok := strings.CutPrefix(value, "gcodes/")
	if !ok || relative == "" || strings.ContainsAny(relative, "\\%\r\n\x00") || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return "", fmt.Errorf("%w: destination must be a canonical path inside gcodes", connectorapi.ErrTransferRefused)
	}
	for _, segment := range strings.Split(relative, "/") {
		if segment == "." || segment == ".." {
			return "", connectorapi.ErrTransferRefused
		}
	}
	if strings.ToLower(path.Ext(relative)) != ".gcode" {
		return "", fmt.Errorf("%w: expected a .gcode file", connectorapi.ErrTransferRefused)
	}
	return relative, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
