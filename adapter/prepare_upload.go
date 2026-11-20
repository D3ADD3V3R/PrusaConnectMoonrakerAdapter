package moonraker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"pcma.local/moonraker/gcode"
)

const maxPreparationBytes int64 = 512 << 20

type preparedContent struct {
	filename string
	size     int64
}

func (p preparedContent) Size() int64 { return p.size }
func (p preparedContent) Open(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(p.filename)
	if err != nil {
		return nil, err
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

type preparedCleanup struct {
	once    sync.Once
	cleanup func() error
	err     error
}

func (p *preparedCleanup) Close() error {
	p.once.Do(func() { p.err = p.cleanup() })
	return p.err
}

// PrepareUpload derives a new execution before the host records or sends its bytes.
// Missing slicer fields independently use the printer's locally confirmed values.
func (s *Session) PrepareUpload(ctx context.Context, up connectorapi.Upload) (connectorapi.Upload, io.Closer, error) {
	if s.executions == nil {
		return up, nil, nil
	}
	if !s.uploads.preparation.TryLock() {
		return connectorapi.Upload{}, nil, connectorapi.TransferBusy.New("another file is being prepared").With(fault.StringParam("path", up.Path)).Wrap(connectorapi.ErrTransferBusy)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			s.uploads.preparation.Unlock()
		}
	}()
	refuse := func(message string) (connectorapi.Upload, io.Closer, error) {
		return connectorapi.Upload{}, nil, connectorapi.TransferRefused.New("G-code preparation was refused").With(fault.StringParam("path", up.Path), fault.StringParam("machine_reason", "preparation_refused"), fault.StringParam("reason", message)).Wrap(connectorapi.ErrTransferRefused)
	}
	if err := connectorapi.RefusePathEscape(up.Path); err != nil {
		return connectorapi.Upload{}, nil, err
	}
	if !strings.EqualFold(path.Ext(up.Path), ".gcode") {
		return refuse("automatic preparation requires plain .gcode")
	}
	if up.Content == nil || up.Content.Size() < 0 || up.Content.Size() > maxPreparationBytes {
		return refuse("G-code preparation requires a file no larger than 512 MiB")
	}
	src, err := up.Content.Open(ctx, 0)
	if err != nil {
		return connectorapi.Upload{}, nil, err
	}
	defer src.Close()
	stop := context.AfterFunc(ctx, func() { _ = src.Close() })
	defer stop()
	input, err := os.CreateTemp("", "pcma-source-*")
	if err != nil {
		return connectorapi.Upload{}, nil, err
	}
	defer os.Remove(input.Name())
	defer input.Close()
	written, err := io.Copy(input, io.LimitReader(&contextReader{ctx, src}, up.Content.Size()+1))
	if err != nil {
		return connectorapi.Upload{}, nil, err
	}
	if written != up.Content.Size() {
		return refuse("source length changed during preparation")
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return connectorapi.Upload{}, nil, err
	}
	artifact, inspectErr := gcode.Inspect(input)
	output, err := os.CreateTemp("", "pcma-prepared-*")
	if err != nil {
		return connectorapi.Upload{}, nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = output.Close()
			_ = os.Remove(output.Name())
		}
	}()
	if inspectErr == nil {
		snapshot, err := s.executionSnapshot(ctx)
		if err != nil {
			return connectorapi.Upload{}, nil, err
		}
		if snapshot.Evo.ExecutionID == artifact.ExecutionID {
			return refuse("execution already used by the printer; upload the original for a new execution")
		}

		if _, err = input.Seek(0, io.SeekStart); err != nil {
			return connectorapi.Upload{}, nil, err
		}
		if _, err = io.Copy(output, &contextReader{ctx, input}); err != nil {
			return connectorapi.Upload{}, nil, err
		}
	} else {
		material, color, err := gcode.SourceMetadata(input)
		if err != nil {
			return refuse("unsupported or ambiguous slicer metadata: " + err.Error())
		}
		if material == "" || color == "" {
			snapshot, err := s.executionSnapshot(ctx)
			if err != nil {
				return connectorapi.Upload{}, nil, err
			}
			if snapshot.Evo.FilamentPhase != "CONFIRMED" || snapshot.Evo.ChangeID == "" || snapshot.Evo.ChangeID != snapshot.Evo.ConfirmedChangeID {
				return refuse("missing metadata: confirm the loaded material and color locally in PCMA first")
			}
			if material == "" {
				material = snapshot.Evo.LoadedMaterial
			}
			if color == "" {
				color = snapshot.Evo.LoadedColor
			}
		}
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return connectorapi.Upload{}, nil, err
		}
		stem := strings.TrimSuffix(path.Base(up.Path), path.Ext(up.Path))
		var label strings.Builder
		for _, char := range stem {
			if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
				label.WriteRune(char)
			} else {
				label.WriteByte('_')
			}
			if label.Len() >= 30 {
				break
			}
		}
		if label.Len() == 0 {
			label.WriteString("job")
		}
		execution := label.String() + "-" + hex.EncodeToString(id[:])
		artifact, err = gcode.Prepare(input, output, gcode.Options{ExecutionID: execution, ProfileID: s.executions.profile, ProfileVersion: s.executions.profileVersion, Material: material, Color: color})
		if err != nil {
			return refuse("could not prepare G-code: " + err.Error())
		}
	}
	if artifact.ProfileID != s.executions.profile || artifact.ProfileVersion != s.executions.profileVersion || !slices.Contains(s.executions.materials, artifact.Material) {
		return refuse("file does not match this printer's material/profile revision")
	}
	if len(artifact.Color) != 8 || !strings.HasPrefix(artifact.Color, "C_") {
		return refuse("missing or unknown color: confirm a loaded RGB color in PCMA")
	}
	if _, err = hex.DecodeString(artifact.Color[2:]); err != nil {
		return refuse("invalid RGB color")
	}
	if err = output.Sync(); err != nil {
		return connectorapi.Upload{}, nil, err
	}
	if err = output.Close(); err != nil {
		return connectorapi.Upload{}, nil, err
	}
	up.Path = "gcodes/pcma/" + artifact.ExecutionID + ".gcode"
	document, err := json.Marshal(artifact)
	if err != nil {
		return connectorapi.Upload{}, nil, err
	}
	// Reserve the identity before another send can replace the same prepared file.
	inserted, err := s.executions.store.db.ExecContext(ctx, `INSERT INTO artifacts(execution,path,document) VALUES(?,?,?) ON CONFLICT DO NOTHING`, artifact.ExecutionID, up.Path, string(document))
	if err != nil {
		return connectorapi.Upload{}, nil, fmt.Errorf("reserve prepared artifact: %w", err)
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return connectorapi.Upload{}, nil, err
	}
	if count != 1 {
		return refuse("prepared execution is already registered; select its printer file or upload the original to create a new execution")
	}
	up.Content = preparedContent{filename: output.Name(), size: artifact.Size}
	keep = true
	handedOff = true
	return up, &preparedCleanup{cleanup: func() error {
		defer s.uploads.preparation.Unlock()
		cleaning, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, err := s.executions.store.db.ExecContext(cleaning, `DELETE FROM artifacts WHERE execution=? AND verified=0 AND NOT EXISTS(SELECT 1 FROM executions WHERE execution=?)`, artifact.ExecutionID, artifact.ExecutionID)
		return errors.Join(os.Remove(output.Name()), err)
	}}, nil
}
