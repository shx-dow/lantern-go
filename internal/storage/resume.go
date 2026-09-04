package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ResumeState struct {
	Code     string `json:"code"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	Offset   int64  `json:"offset"`
}

// LoadResume returns a valid resume state. Invalid or stale state is removed
// and treated as no resumable transfer.
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

// SaveResume atomically replaces the resume state with a private file.
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

	if err := tmp.Chmod(0600); err != nil {
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
		state.FileName != "" &&
		state.FileName == filepath.Base(state.FileName) &&
		state.FileSize >= 0 &&
		state.Offset >= 0 &&
		state.Offset <= state.FileSize
}
