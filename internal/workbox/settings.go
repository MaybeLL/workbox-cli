package workbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	googleuuid "github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type localSettings struct {
	Version       int             `json:"settings_version"`
	Revision      string          `json:"revision"`
	InitializedAt string          `json:"initialized_at"`
	Storage       storageSettings `json:"storage"`
	Skill         skillPreference `json:"skill_preference"`
}
type storageSettings struct {
	Backend string `json:"backend"`
	DSNFile string `json:"dsn_file,omitempty"`
}
type skillPreference struct {
	Install bool    `json:"install"`
	Parent  *string `json:"parent_dir"`
}
type settingsRecord struct {
	value  *localSettings
	digest [32]byte
	exists bool
}

// Canonicalize existing ancestors as well as an existing root. This preserves
// ordinary directory aliases while pinning the path for this invocation.
func settingsRoot() (string, *Failure) {
	p, e := configPath()
	if e != nil {
		return "", e
	}
	return canonicalPath(filepath.Dir(p))
}
func canonicalPath(p string) (string, *Failure) {
	tail := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", ioFailure("Could not resolve configuration path")
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", ioFailure("Could not resolve configuration path")
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
}
func privateInfo(info os.FileInfo, directory bool) bool {
	return info.Mode().Perm()&0077 == 0 && ((directory && info.IsDir()) || (!directory && info.Mode().IsRegular()))
}
func checkedFile(path string, private bool) ([]byte, *Failure) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fail("CONFIG_INVALID", "Configuration file must not be a symbolic link")
		}
		return nil, ioFailure("Could not read configuration file")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, ioFailure("Could not inspect configuration file")
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		return nil, ioFailure("Could not inspect configuration owner")
	}
	if !info.Mode().IsRegular() || (private && (!privateInfo(info, false) || stat.Uid != uint32(os.Geteuid()))) {
		return nil, fail("CONFIG_INVALID", "Configuration must be a private, current-user-owned regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return nil, ioFailure("Could not read configuration file")
	}
	if len(raw) > 1024*1024 || !utf8.Valid(raw) {
		return nil, fail("CONFIG_INVALID", "Configuration file is too large or is not UTF-8")
	}
	return raw, nil
}
func privateDir(path string) *Failure {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fail("CONFIG_INVALID", "Saved credentials directory is missing; repair with workbox init")
		}
		return ioFailure("Could not inspect credentials directory")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) {
		return fail("CONFIG_INVALID", "Credentials directory must be private and owned by the current user")
	}
	return nil
}
func loadSettings(root string) (settingsRecord, *Failure) {
	r := settingsRecord{}
	path := filepath.Join(root, "settings.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return r, nil
	} else if err != nil {
		return r, ioFailure("Could not inspect settings.json")
	}
	raw, e := checkedFile(path, true)
	if e != nil {
		return r, e
	}
	r.exists = true
	r.digest = sha256.Sum256(raw)
	bad := fail("CONFIG_INVALID", "Invalid settings.json; back up and move this file aside before running workbox init")
	bad.Details["path"] = path
	if checkJSON(raw) != nil {
		return r, bad
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return r, bad
	}
	for _, key := range []string{"settings_version", "revision", "initialized_at", "storage", "skill_preference"} {
		if fields[key] == nil || string(fields[key]) == "null" {
			return r, bad
		}
	}
	var v int
	if json.Unmarshal(fields["settings_version"], &v) != nil {
		return r, bad
	}
	if v != 1 {
		e := fail("SCHEMA_UNSUPPORTED", "Unsupported settings version; use a compatible Workbox version")
		e.Details = map[string]any{"scope": "settings", "path": path}
		return r, e
	}
	var value localSettings
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil {
		return r, bad
	}
	id, err := googleuuid.Parse(value.Revision)
	if err != nil || id.String() != value.Revision {
		return r, bad
	}
	timestamp, err := time.Parse(time.RFC3339, value.InitializedAt)
	if err != nil || timestamp.Format(time.RFC3339) != value.InitializedAt || !strings.HasSuffix(value.InitializedAt, "Z") {
		return r, bad
	}
	var storage, skill map[string]json.RawMessage
	_ = json.Unmarshal(fields["storage"], &storage)
	_ = json.Unmarshal(fields["skill_preference"], &skill)
	if len(skill) != 2 || skill["install"] == nil || string(skill["install"]) == "null" || skill["parent_dir"] == nil {
		return r, bad
	}
	if value.Skill.Install {
		if value.Skill.Parent == nil || !filepath.IsAbs(*value.Skill.Parent) {
			return r, bad
		}
	} else if value.Skill.Parent != nil {
		return r, bad
	}
	switch value.Storage.Backend {
	case "sqlite":
		if len(storage) != 1 || storage["backend"] == nil {
			return r, bad
		}
	case "mysql":
		if len(storage) != 2 || storage["backend"] == nil || storage["dsn_file"] == nil || value.Storage.DSNFile != filepath.Join(root, "credentials", "mysql-"+value.Revision+".dsn") {
			return r, bad
		}
	default:
		return r, bad
	}
	r.value = &value
	return r, nil
}
func settingsUnchanged(root string, old settingsRecord) *Failure {
	now, e := loadSettings(root)
	if e != nil || old.exists != now.exists || old.digest != now.digest {
		return fail("CONFIG_CONFLICT", "Settings changed during initialization; run workbox init again")
	}
	return nil
}
func savedStore(root string, saved *localSettings, overrides bool) (configStore, *Failure) {
	backend := saved.Storage.Backend
	if overrides && os.Getenv("WORKBOX_DB") != "" {
		backend = os.Getenv("WORKBOX_DB")
	}
	switch backend {
	case "sqlite":
		return &sqliteStore{path: filepath.Join(root, "config.db")}, nil
	case "mysql":
		if overrides && (os.Getenv("WORKBOX_MYSQL_DSN") != "" || os.Getenv("WORKBOX_MYSQL_DSN_FILE") != "") {
			return configuredMySQLStore()
		}
		if saved.Storage.Backend != "mysql" {
			return nil, invalid("MySQL requires an explicit connection")
		}
		if e := privateDir(filepath.Join(root, "credentials")); e != nil {
			return nil, e
		}
		raw, e := checkedFile(saved.Storage.DSNFile, true)
		if e != nil {
			if _, err := os.Lstat(saved.Storage.DSNFile); errors.Is(err, os.ErrNotExist) {
				return nil, fail("CONFIG_INVALID", "Saved MySQL credentials are missing; repair with workbox init")
			}
			return nil, e
		}
		return newMySQLStore(strings.TrimSpace(string(raw)))
	default:
		return nil, invalid("WORKBOX_DB must be sqlite or mysql")
	}
}
func initializedStore() (configStore, *Failure) {
	root, e := settingsRoot()
	if e != nil {
		return nil, e
	}
	r, e := loadSettings(root)
	if e != nil {
		return nil, e
	}
	if !r.exists {
		e := fail("NOT_INITIALIZED", "Workbox is not initialized; run workbox init first")
		e.Details = map[string]any{"path": filepath.Join(root, "settings.json"), "next_step": "workbox init"}
		return nil, e
	}
	return savedStore(root, r.value, true)
}
func acquireInitLock(ctx context.Context, root string) (func(), *Failure) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, ioFailure("Could not create configuration directory")
	}
	fd, err := unix.Open(filepath.Join(root, "init.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ioFailure("Could not open init lock")
	}
	f := os.NewFile(uintptr(fd), "init.lock")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, fail("CONFIG_INVALID", "Unsafe init lock file")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, ioFailure("Could not lock initialization")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fail("INTERRUPTED", "Initialization interrupted")
		case <-deadline.C:
			f.Close()
			return nil, fail("CONFIG_CONFLICT", "Another init is running; retry later")
		case <-tick.C:
		}
	}
}

// published distinguishes a visible rename/link from failure before publication.
func atomicPrivateFile(path string, raw []byte, replace bool) (published bool, failure *Failure) {
	return publishPrivateFile(path, raw, replace, func(dir *os.File) error { return dir.Sync() })
}

// The durability operation is explicit so failure after publication can be
// tested independently of filesystem-specific fsync support.
func publishPrivateFile(path string, raw []byte, replace bool, syncDirectory func(*os.File) error) (published bool, failure *Failure) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".workbox-init-")
	if err != nil {
		return false, ioFailure("Could not stage configuration")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return false, ioFailure("Could not sync configuration")
	}
	if err = f.Close(); err != nil {
		return false, ioFailure("Could not close configuration")
	}
	if replace {
		err = os.Rename(f.Name(), path)
	} else {
		err = os.Link(f.Name(), path)
	}
	if err != nil {
		return false, ioFailure("Could not publish configuration")
	}
	d, err := os.Open(dir)
	if err != nil {
		return true, ioFailure("Configuration is visible but durability is unknown")
	}
	defer d.Close()
	if err = syncDirectory(d); err != nil {
		return true, ioFailure("Configuration is visible but durability is unknown")
	}
	return true, nil
}
