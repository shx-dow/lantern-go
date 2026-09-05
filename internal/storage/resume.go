package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResumeState checkpoints a partial download so a retry continues at
// Offset instead of restarting.
type ResumeState struct {
	Code     string `json:"code"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	Offset   int64  `json:"offset"`
}

// Permissions: resume state and advertisements hold transfer codes, which
// are the capability to fetch the file, so they are owner-only. Data
// directories and received files stay world-readable to match umask
// expectations for shared downloads.
const (
	PrivateFilePerm = 0600
	PublicFilePerm  = 0644
	PrivateDirPerm  = 0700
	PublicDirPerm   = 0755
)

// CheckFileName rejects empty names, dot entries, and anything with a
// path separator so remote-provided names cannot escape the output dir.
func CheckFileName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return fmt.Errorf("invalid file name %q", name)
	}
	return nil
}

// LoadResume returns the checkpoint for code, or ok=false when there is
// none. Corrupt or stale entries are removed and treated as no resume.
func LoadResume(outputDir, code string) (ResumeState, bool, error) {
	path, err := statePath(outputDir, code)
	if err != nil {
		return ResumeState{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ResumeState{}, false, nil
	}
	if err != nil {
		return ResumeState{}, false, fmt.Errorf("read resume state: %w", err)
	}

	var state ResumeState
	if err := json.Unmarshal(data, &state); err != nil || !validState(state, code) {
		_ = os.Remove(path)
		return ResumeState{}, false, nil
	}

	partial, err := PartialPath(outputDir, state.Code, state.FileName)
	if err != nil {
		_ = os.Remove(path)
		return ResumeState{}, false, nil
	}
	info, err := os.Stat(partial)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() < state.Offset) {
		_ = os.Remove(path)
		return ResumeState{}, false, nil
	}
	if err != nil {
		return ResumeState{}, false, fmt.Errorf("stat resumed file: %w", err)
	}

	return state, true, nil
}

// SaveResume atomically replaces the checkpoint via temp file + rename so
// crashes never leave a half-written state.
func SaveResume(outputDir string, state ResumeState) error {
	if _, err := safeCode(state.Code); err != nil {
		return err
	}
	if !validState(state, state.Code) {
		return errors.New("invalid resume state")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal resume state: %w", err)
	}

	tmp, err := os.CreateTemp(outputDir, ".lantern-state-*")
	if err != nil {
		return fmt.Errorf("create temporary resume state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(PrivateFilePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("protect temporary resume state: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write resume state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync resume state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close resume state: %w", err)
	}
	dest, err := statePath(outputDir, state.Code)
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("replace resume state: %w", err)
	}
	return nil
}

// ClearResume removes the checkpoint; a missing one is not an error.
func ClearResume(outputDir, code string) error {
	path, err := statePath(outputDir, code)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// PartialPath is the on-disk location of the in-progress download for
// code and fileName. It errors on codes that would escape outputDir.
func PartialPath(outputDir, code, fileName string) (string, error) {
	safe, err := safeCode(code)
	if err != nil {
		return "", err
	}
	return filepath.Join(outputDir, ".lantern-"+safe+"-"+filepath.Base(fileName)+".part"), nil
}

func statePath(outputDir, code string) (string, error) {
	safe, err := safeCode(code)
	if err != nil {
		return "", err
	}
	return filepath.Join(outputDir, "."+safe+".lantern-state"), nil
}

func safeCode(code string) (string, error) {
	if code == "" || code == "." || code == ".." || filepath.Base(code) != code {
		return "", fmt.Errorf("invalid code %q", code)
	}
	return code, nil
}

func validState(state ResumeState, code string) bool {
	return state.Code == code &&
		CheckFileName(state.FileName) == nil &&
		state.FileSize >= 0 &&
		state.Offset >= 0 &&
		state.Offset <= state.FileSize
}
