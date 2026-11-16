package workbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MaybeLL/workbox-cli/skills"
)

func skillBundle() (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(skills.Files, "workbox", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := skills.Files.ReadFile(path)
		if err == nil {
			files[strings.TrimPrefix(path, "workbox/")] = body
		}
		return err
	})
	return files, err
}

// Compare the entire tree, including unexpected files and links, before calling
// an installation unchanged. Existing content is never repaired or overwritten.
func sameSkill(root *os.Root, files map[string][]byte) bool {
	return sameSkillHashes(root, skillHashes(files))
}

func sameSkillHashes(root *os.Root, files map[string]string) bool {
	count := 0
	err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if entry.IsDir() {
			for name := range files {
				if strings.HasPrefix(name, path+"/") {
					return nil
				}
			}
			return fs.ErrInvalid
		}
		hash, known := files[path]
		if !known || !entry.Type().IsRegular() {
			return fs.ErrInvalid
		}
		actual, err := root.ReadFile(path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(actual)) != hash {
			return fs.ErrInvalid
		}
		count++
		return nil
	})
	return err == nil && count == len(files)
}

func existingSkill(parent *os.Root, files map[string][]byte) (bool, *Failure) {
	info, err := parent.Lstat("workbox")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, ioFailure("Could not inspect the skill destination")
	}
	conflict := fail("SKILL_CONFLICT", "Existing workbox skill differs or cannot be verified; back it up and move it aside, or choose another --path. Nothing was overwritten")
	if !info.IsDir() {
		return true, conflict
	}
	root, err := parent.OpenRoot("workbox")
	if err != nil {
		return true, ioFailure("Could not open the existing skill")
	}
	defer root.Close()
	if actual, err := root.Stat("."); err != nil || !os.SameFile(info, actual) || !sameSkill(root, files) {
		return true, conflict
	}
	return true, nil
}

// A complete current bundle can be adopted; a different tree must match its receipt.
func inspectInstall(parent *os.Root, dir string, files map[string][]byte) (bool, *uninstallSkill, *Failure) {
	exists, conflict := existingSkill(parent, files)
	if conflict == nil || conflict.Code != "SKILL_CONFLICT" {
		return exists, nil, conflict
	}
	hashes, e := installedHashes(dir)
	if e != nil {
		return true, nil, e
	}
	if hashes == nil {
		return true, nil, conflict
	}
	old, e := inspectRemoval(filepath.Join(dir, "workbox"), hashes)
	if e != nil || old.State != "pending" {
		return true, nil, conflict
	}
	return true, old, nil
}

func installSkill(ctx context.Context, flags map[string]string) (data any, failure *Failure) {
	raw, custom := flags["path"]
	if custom && strings.TrimSpace(raw) == "" {
		return nil, fail("PATH_INVALID", "--path must name a skills directory")
	}
	if !custom {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, ioFailure("Could not locate user home; use --path to choose a skills directory")
		}
		raw = filepath.Join(home, ".agents", "skills")
	}
	dir, e := absolute(raw)
	if e != nil {
		return nil, e
	}
	target := filepath.Join(dir, "workbox")
	defer func() {
		if failure != nil {
			failure.Details["path"] = target
		}
	}()
	files, err := skillBundle()
	if err != nil {
		return nil, ioFailure("Could not read the bundled skill")
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Operation interrupted")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, ioFailure("Could not create the skills directory")
	}
	parent, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ioFailure("Could not open the skills directory")
	}
	defer parent.Close()
	unlock, e := lockSkillState(ctx)
	if e != nil {
		return nil, e
	}
	defer unlock()
	result := map[string]any{"skill": "workbox", "path": target, "state": "unchanged", "next_step": "Use $workbox in your next Codex message; restart Codex if it does not appear"}
	recordPublished := func() (any, *Failure) {
		if e := recordSkill(dir, files); e != nil {
			result["publication"] = "published"
			result["receipt"] = "failed"
			result["next_step"] = "Repair the installation record and rerun the same skill install command"
			result["retry_args"] = []string{"workbox", "skill", "install", "--path", dir}
			e.Message = "Current Skill files are installed but the receipt was not saved: " + e.Message
			e.Data = result
			return result, e
		}
		return result, nil
	}
	exists, old, e := inspectInstall(parent, dir, files)
	if e != nil {
		return nil, e
	}
	if exists && old == nil {
		return recordPublished()
	}
	id, e := uuid()
	if e != nil {
		return nil, e
	}
	stage := ".workbox-skill-" + id
	if err := parent.Mkdir(stage, 0755); err != nil {
		return nil, ioFailure("Could not stage the skill")
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = parent.RemoveAll(stage)
		}
	}()
	root, err := parent.OpenRoot(stage)
	if err != nil {
		return nil, ioFailure("Could not open the staged skill")
	}
	defer root.Close()
	for _, name := range names(files) {
		if ctx.Err() != nil {
			return nil, fail("INTERRUPTED", "Operation interrupted")
		}
		if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return nil, ioFailure("Could not stage the skill directories")
		}
		if err := root.WriteFile(name, files[name], 0644); err != nil {
			return nil, ioFailure("Could not stage the skill files")
		}
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Operation interrupted")
	}
	if old != nil {
		current, e := inspectRemoval(target, old.files)
		if e != nil || current.State != "pending" || !os.SameFile(current.info, old.info) {
			return nil, fail("SKILL_CONFLICT", "Installed Skill changed before upgrade")
		}
		if err := exchangeDirs(parent, stage, "workbox"); err != nil {
			return nil, ioFailure("Could not publish Skill upgrade")
		}
		// The exchange placed the previous installation in stage. Preserve anything
		// that raced the final check, including user files in a replaced directory.
		cleanupStage = false
		moved, e := inspectRemoval(filepath.Join(dir, stage), old.files)
		if e != nil || moved.State != "pending" || !os.SameFile(moved.info, old.info) {
			if err := exchangeDirs(parent, stage, "workbox"); err != nil {
				return nil, ioFailure("Skill changed during upgrade; inspect retained directory " + stage)
			}
			cleanupStage = true
			return nil, fail("SKILL_CONFLICT", "Skill changed during upgrade")
		}
		cleanupStage = true
		result["state"] = "upgraded"
		return recordPublished()
	}
	// Publish the complete tree without ever replacing an existing target.
	// os.Root.Rename cannot portably replace even an empty reserved directory.
	if e := recordSkill(dir, files); e != nil {
		return nil, e
	}
	if err := publishDir(parent, stage, "workbox"); err != nil {
		if errors.Is(err, os.ErrExist) {
			if exists, e := existingSkill(parent, files); exists || e != nil {
				return result, e
			}
		}
		failure := ioFailure("Could not publish the skill")
		failure.Details["cause"] = err.Error()
		return nil, failure
	}
	result["state"] = "installed"
	return result, nil
}
