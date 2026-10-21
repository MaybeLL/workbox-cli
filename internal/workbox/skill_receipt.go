package workbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type skillReceipt struct {
	Version int               `json:"version"`
	Path    string            `json:"path"`
	Files   map[string]string `json:"files"`
}

func skillHashes(files map[string][]byte) map[string]string {
	result := map[string]string{}
	for path, body := range files {
		result[path] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	return result
}

func installStateDir() (string, *Failure) {
	if raw := os.Getenv("WORKBOX_STATE_HOME"); raw != "" {
		return absolute(raw)
	}
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ioFailure("Could not locate installation records; set WORKBOX_STATE_HOME")
		}
		root = filepath.Join(home, ".local", "state")
	}
	return absolute(filepath.Join(root, "workbox"))
}

func receiptName(path string) string {
	return fmt.Sprintf("skill-%x.json", sha256.Sum256([]byte(path)))
}

func validReceipt(r skillReceipt) bool {
	if r.Version != 1 || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || filepath.Base(r.Path) != "workbox" || r.Files["SKILL.md"] == "" {
		return false
	}
	for name, hash := range r.Files {
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, "\\") {
			return false
		}
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != sha256.Size {
			return false
		}
	}
	return true
}

// One atomic receipt per canonical target avoids losing custom paths when
// installers run concurrently, without sharing or migrating the configuration DB.
func recordSkill(dir string, files map[string][]byte) *Failure {
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ioFailure("Could not resolve the skill installation directory")
	}
	receipt := skillReceipt{1, filepath.Join(canonical, "workbox"), skillHashes(files)}
	body, err := json.Marshal(receipt)
	if err != nil {
		return ioFailure("Could not encode the skill installation record")
	}
	state, e := installStateDir()
	if e != nil {
		return e
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return ioFailure("Could not create installation records; set WORKBOX_STATE_HOME to a writable directory")
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return ioFailure("Could not open installation records")
	}
	defer root.Close()
	name := receiptName(receipt.Path)
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return invalid("Skill installation record is not a regular file")
		}
		old, err := root.ReadFile(name)
		var previous skillReceipt
		if err != nil || json.Unmarshal(old, &previous) != nil || !validReceipt(previous) || previous.Path != receipt.Path {
			return invalid("Existing skill installation record is invalid")
		}
		if string(old) == string(body) {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ioFailure("Could not inspect skill installation record")
	}
	id, e := uuid()
	if e != nil {
		return e
	}
	temp := ".skill-record-" + id
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ioFailure("Could not stage the skill installation record")
	}
	defer root.Remove(temp)
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || root.Rename(temp, name) != nil {
		return ioFailure("Could not save the skill installation record")
	}
	return nil
}
