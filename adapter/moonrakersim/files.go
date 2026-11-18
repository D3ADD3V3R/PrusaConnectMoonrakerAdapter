package moonrakersim

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
)

func (h *Host) seedFiles(files map[string][]byte) {
	if files == nil {
		files = map[string][]byte{"sample.gcode": []byte("; simulator fixture\n")}
	}
	for name, data := range files {
		h.files[name] = bytes.Clone(data)
		h.addParents(name)
	}
}

// ReplaceFile simulates an out-of-band change made by another Moonraker client.
func (h *Host) ReplaceFile(name string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[name] = bytes.Clone(data)
	h.addParents(name)
}

func (h *Host) addParents(name string) {
	for dir := path.Dir(name); dir != "." && dir != "/"; dir = path.Dir(dir) {
		h.directories[dir] = true
	}
}

func (h *Host) permissions() string {
	if h.readOnly {
		return "r"
	}
	return "rw"
}

func (h *Host) roots() []map[string]any {
	return []map[string]any{{"name": "gcodes", "permissions": h.permissions()}}
}

func (h *Host) httpRoots(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"result": h.roots()})
}

func (h *Host) fileAnswer(id uint64, method string, params json.RawMessage) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if method == "server.files.roots" {
		return resultFrame(id, h.roots())
	}
	var args struct {
		Path  string `json:"path"`
		Force bool   `json:"force"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return errorFrame(id, 400, "Invalid file arguments")
	}
	name := strings.TrimPrefix(args.Path, "gcodes")
	name = strings.TrimPrefix(name, "/")
	if args.Path != "gcodes" && !strings.HasPrefix(args.Path, "gcodes/") || path.Clean(name) != name && name != "" || strings.Contains(name, "..") {
		return errorFrame(id, 400, "Invalid gcodes path")
	}
	if method == "server.files.get_directory" {
		if !h.directories[name] {
			return errorFrame(id, 400, "Directory not found")
		}
		files := []map[string]any{}
		dirs := []map[string]any{}
		for filename, data := range h.files {
			parent := path.Dir(filename)
			if parent == "." {
				parent = ""
			}
			if parent == name {
				files = append(files, map[string]any{"filename": path.Base(filename), "size": len(data), "modified": 1, "permissions": h.permissions()})
			}
		}
		for dirname := range h.directories {
			parent := path.Dir(dirname)
			if parent == "." {
				parent = ""
			}
			if dirname != "" && parent == name {
				dirs = append(dirs, map[string]any{"dirname": path.Base(dirname), "modified": 1, "permissions": h.permissions()})
			}
		}
		return resultFrame(id, map[string]any{"files": files, "dirs": dirs, "root_info": map[string]any{"name": "gcodes", "permissions": h.permissions()}})
	}
	if h.readOnly {
		return errorFrame(id, 403, "Read only")
	}
	action := ""
	switch method {
	case "server.files.post_directory":
		_, file := h.files[name]
		if name == "" || h.directories[name] || file {
			return errorFrame(id, 400, "Destination exists")
		}
		parent := path.Dir(name)
		if parent == "." {
			parent = ""
		}
		if !h.directories[parent] {
			return errorFrame(id, 400, "Parent not found")
		}
		h.directories[name] = true
		action = "create_dir"
	case "server.files.delete_file":
		if _, exists := h.files[name]; !exists {
			return errorFrame(id, 400, "File not found")
		}
		delete(h.files, name)
		action = "delete_file"
	case "server.files.delete_directory":
		if name == "" || !h.directories[name] {
			return errorFrame(id, 400, "Directory not found")
		}
		for filename := range h.files {
			if strings.HasPrefix(filename, name+"/") {
				return errorFrame(id, 400, "Directory not empty")
			}
		}
		for dirname := range h.directories {
			if strings.HasPrefix(dirname, name+"/") {
				return errorFrame(id, 400, "Directory not empty")
			}
		}
		for filename := range h.files {
			if strings.HasPrefix(filename, name+"/") {
				return errorFrame(id, 400, "Directory is not empty")
			}
		}
		for dirname := range h.directories {
			if strings.HasPrefix(dirname, name+"/") {
				return errorFrame(id, 400, "Directory is not empty")
			}
		}
		delete(h.directories, name)
		action = "delete_dir"
	default:
		return errorFrame(id, methodNotFound, "Unknown file method")
	}
	return resultFrame(id, map[string]any{"item": map[string]any{"root": "gcodes", "path": name}, "action": action})
}

func (h *Host) httpUpload(w http.ResponseWriter, r *http.Request) {
	if h.readOnly {
		http.Error(w, "Read only", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "Invalid upload", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	if r.FormValue("root") != "gcodes" || r.FormValue("print") != "false" {
		http.Error(w, "Expected storage-only gcodes upload", 400)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Missing file", 400)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Unreadable file", 400)
		return
	}
	if r.FormValue("checksum") != fmt.Sprintf("%x", sha256.Sum256(data)) {
		http.Error(w, "Checksum mismatch", 422)
		return
	}
	name := path.Join(r.FormValue("path"), header.Filename)
	if strings.HasPrefix(name, "/") || slices.Contains(strings.Split(name, "/"), "..") {
		http.Error(w, "Invalid path", 400)
		return
	}
	h.mu.Lock()
	h.files[name] = data
	h.addParents(name)
	h.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"item":   map[string]any{"root": "gcodes", "path": name, "size": len(data)},
		"action": "create_file", "print_started": false, "print_queued": false,
	})
}

func (h *Host) httpDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/server/files/gcodes/")
	h.mu.Lock()
	data, exists := h.files[name]
	data = bytes.Clone(data)
	h.mu.Unlock()
	if !exists {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}
