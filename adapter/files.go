package moonraker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

type fileState struct{ roots []connectorapi.Storage }

type directoryEntry struct {
	Filename    string  `json:"filename"`
	Dirname     string  `json:"dirname"`
	Size        int64   `json:"size"`
	Modified    float64 `json:"modified"`
	Permissions string  `json:"permissions"`
}

func directoryListing(value string, data json.RawMessage) (connectorapi.Listing, error) {
	var reply struct {
		Files []directoryEntry `json:"files"`
		Dirs  []directoryEntry `json:"dirs"`
		Root  struct {
			Permissions string `json:"permissions"`
		} `json:"root_info"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return connectorapi.Listing{}, err
	}
	out := connectorapi.Listing{Directory: connectorapi.File{Path: value, Name: path.Base(value), Folder: true, ReadOnly: !strings.Contains(reply.Root.Permissions, "w")}}
	seen := map[string]bool{}
	for _, group := range []struct {
		entries []directoryEntry
		folder  bool
	}{{reply.Dirs, true}, {reply.Files, false}} {
		for _, entry := range group.entries {
			name := entry.Filename
			if group.folder {
				name = entry.Dirname
			}
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\%\r\n\x00") || entry.Size < 0 || entry.Modified < 0 || entry.Modified >= math.MaxInt64 || seen[name] {
				return connectorapi.Listing{}, fmt.Errorf("%w: invalid directory entry", connectorapi.ErrListingRefused)
			}
			seen[name] = true
			if !strings.Contains(entry.Permissions, "r") {
				continue
			}
			out.Entries = append(out.Entries, connectorapi.File{Path: path.Join(value, name), Name: name, Folder: group.folder, Size: entry.Size, Modified: int64(entry.Modified), ReadOnly: !strings.Contains(entry.Permissions, "w")})
		}
	}
	return out, nil
}

type fileRoot struct {
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
}

var _ connectorapi.Filesystem = (*Session)(nil)
var _ connectorapi.FileManager = (*Session)(nil)

func filePathArgument() connectorapi.Value {
	return connectorapi.Value{Flag: true, Kind: connectorapi.KindString, Required: true, HasLimit: true, Limit: 1024}
}

func (s *Session) discoverFiles(ctx context.Context, offered connectorapi.Set, objects []string) error {
	if !slices.Contains(objects, "virtual_sdcard") || !offered.Has(connectorapi.VerbSendInfo) {
		return nil
	}
	var roots []fileRoot
	if err := s.callSync(ctx, "server.files.roots", nil, &roots); err != nil {
		return err
	}
	for _, root := range roots {
		if root.Name != "gcodes" || !strings.Contains(root.Permissions, "r") {
			continue
		}
		readOnly := !strings.Contains(root.Permissions, "w")
		s.files.roots = []connectorapi.Storage{{Mountpoint: "/gcodes", Name: "gcodes", Type: "virtual_sdcard", ReadOnly: readOnly}}
		carry(s.probe, offered, connectorapi.VerbSendInfo, connectorapi.VerbSendFileInfo,
			connectorapi.ArgumentKey(connectorapi.VerbSendFileInfo, "path"))
		if !readOnly {
			for _, verb := range []connectorapi.Capability{connectorapi.VerbCreateFolder, connectorapi.VerbDeleteFile, connectorapi.VerbDeleteFolder} {
				carry(s.probe, offered, verb, connectorapi.ArgumentKey(verb, "path"))
			}
		}
	}
	return nil
}

// Storages returns the gcodes root captured during the session handshake.
func (s *Session) Storages(ctx context.Context) ([]connectorapi.Storage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.caps.Has(connectorapi.VerbSendInfo) {
		return nil, connectorapi.ErrNoFilesystem
	}
	return slices.Clone(s.files.roots), nil
}

// List reads one directory or returns the matching file from its parent directory.
func (s *Session) List(ctx context.Context, value string) (connectorapi.Listing, error) {
	if !s.caps.Has(connectorapi.VerbSendFileInfo) {
		return connectorapi.Listing{}, connectorapi.ErrNoFilesystem
	}
	if err := storagePath(value, true); err != nil {
		return connectorapi.Listing{}, err
	}
	if value == "" {
		out := connectorapi.Listing{Directory: connectorapi.File{Folder: true}}
		for _, root := range s.files.roots {
			out.Entries = append(out.Entries, connectorapi.File{Path: root.Name, Name: root.Name, Folder: true, ReadOnly: root.ReadOnly})
		}
		return out, nil
	}
	result, refusal, err := s.call(ctx, "server.files.get_directory", map[string]any{"path": value, "extended": false})
	if err != nil {
		return connectorapi.Listing{}, err
	}
	if refusal == nil {
		return directoryListing(value, result)
	}
	// Moonraker accepts directories here. A file is resolved through its parent.
	if value == "gcodes" {
		return connectorapi.Listing{}, fmt.Errorf("%w: %s", connectorapi.ErrListingRefused, refusal.Message)
	}
	parent, err := s.List(ctx, path.Dir(value))
	if err != nil {
		return connectorapi.Listing{}, err
	}
	for _, file := range parent.Entries {
		if file.Path == value && !file.Folder {
			return connectorapi.Listing{Directory: file}, nil
		}
	}
	return connectorapi.Listing{}, fmt.Errorf("%w: path not present", connectorapi.ErrListingRefused)
}

func storagePath(value string, allowRoot bool) error {
	if err := connectorapi.RefusePathEscape(value); err != nil {
		return err
	}
	if allowRoot && (value == "" || value == "gcodes") {
		return nil
	}
	if len(value) > 1024 || !strings.HasPrefix(value, "gcodes/") || strings.HasSuffix(value, "/") || path.Clean(value) != value || strings.ContainsAny(value, "\\%\r\n\x00") {
		return fmt.Errorf("%w: expected a canonical gcodes path", connectorapi.ErrListingRefused)
	}
	return nil
}

func (s *Session) fileMutation(ctx context.Context, verb connectorapi.Capability, value, method, action string) error {
	if !s.caps.Has(verb) {
		return connectorapi.ErrNoFileManager
	}
	if err := storagePath(value, false); err != nil {
		return err
	}
	if !s.uploads.transfer.TryLock() {
		return connectorapi.ErrTransferBusy
	}
	defer s.uploads.transfer.Unlock()
	params := map[string]any{"path": value}
	if verb == connectorapi.VerbDeleteFolder {
		params["force"] = false
	}
	raw, refusal, err := s.call(ctx, method, params)
	if err != nil {
		return err
	}
	if refusal != nil {
		return fmt.Errorf("%w: %s", connectorapi.ErrFileChangeRefused, refusal.Message)
	}
	var reply struct {
		Item struct {
			Root string `json:"root"`
			Path string `json:"path"`
		} `json:"item"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return err
	}
	if reply.Item.Root != "gcodes" || reply.Item.Path != strings.TrimPrefix(value, "gcodes/") || reply.Action != action {
		return fmt.Errorf("%w: unexpected file operation acknowledgement", connectorapi.ErrFileChangeRefused)
	}
	return nil
}

func (s *Session) CreateFolder(ctx context.Context, value string) error {
	return s.fileMutation(ctx, connectorapi.VerbCreateFolder, value, "server.files.post_directory", "create_dir")
}

func (s *Session) DeleteFile(ctx context.Context, value string) error {
	return s.fileMutation(ctx, connectorapi.VerbDeleteFile, value, "server.files.delete_file", "delete_file")
}

func (s *Session) DeleteFolder(ctx context.Context, value string) error {
	return s.fileMutation(ctx, connectorapi.VerbDeleteFolder, value, "server.files.delete_directory", "delete_dir")
}

func (s *Session) fileCommand(ctx context.Context, command connectorapi.Command) (connectorapi.Result, bool, error) {
	var value any
	var err error
	switch command.Verb {
	case connectorapi.VerbSendInfo:
		value, err = s.Storages(ctx)
	case connectorapi.VerbSendFileInfo:
		value, err = s.List(ctx, command.Args["path"])
	case connectorapi.VerbCreateFolder:
		err = s.CreateFolder(ctx, command.Args["path"])
	case connectorapi.VerbDeleteFile:
		err = s.DeleteFile(ctx, command.Args["path"])
	case connectorapi.VerbDeleteFolder:
		err = s.DeleteFolder(ctx, command.Args["path"])
	default:
		return connectorapi.Result{}, false, nil
	}
	if err != nil {
		return connectorapi.Result{}, true, err
	}
	raw, err := json.Marshal(value)
	return connectorapi.Result{OK: err == nil, Raw: raw}, true, err
}
