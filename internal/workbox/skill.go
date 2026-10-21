package workbox

import (
	"bytes"
	"context"
	"errors"
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
		body, known := files[path]
		if !known || !entry.Type().IsRegular() {
			return fs.ErrInvalid
		}
		actual, err := root.ReadFile(path)
		if err != nil || !bytes.Equal(body, actual) {
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
	result := map[string]any{"skill": "workbox", "path": target, "state": "unchanged", "next_step": "Use $workbox in your next Codex message; restart Codex if it does not appear"}
	if exists, e := existingSkill(parent, files); exists || e != nil {
		return result, e
	}
	id, e := uuid()
	if e != nil {
		return nil, e
	}
	stage := ".workbox-skill-" + id
	if err := parent.Mkdir(stage, 0755); err != nil {
		return nil, ioFailure("Could not stage the skill")
	}
	defer parent.RemoveAll(stage)
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
	// Reserve the destination exclusively, then atomically replace our empty
	// reservation with the complete tree. Never rename over a preexisting target.
	if err := parent.Mkdir("workbox", 0755); err != nil {
		if errors.Is(err, os.ErrExist) {
			if exists, e := existingSkill(parent, files); exists || e != nil {
				return result, e
			}
		}
		return nil, ioFailure("Could not reserve the skill destination; retry the command")
	}
	if err := parent.Rename(stage, "workbox"); err != nil {
		_ = parent.Remove("workbox") // Only removes the empty reservation.
		return nil, ioFailure("Could not publish the skill")
	}
	result["state"] = "installed"
	return result, nil
}
