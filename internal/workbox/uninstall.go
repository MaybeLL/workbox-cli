package workbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type uninstallCLI struct {
	Method         string `json:"method"`
	Binary         string `json:"binary"`
	Link           string `json:"link,omitempty"`
	State          string `json:"state"`
	brew, rack     string
	info, linkInfo os.FileInfo
}

type uninstallSkill struct {
	Path  string `json:"path"`
	State string `json:"state"`
	files map[string]string
	info  os.FileInfo
}

type uninstallResult struct {
	State         string            `json:"state"`
	CLI           uninstallCLI      `json:"cli"`
	Skills        []*uninstallSkill `json:"skills"`
	Configuration string            `json:"configuration"`
	Workspaces    string            `json:"workspaces"`
}

type receiptFile struct {
	name string
	body []byte
	info os.FileInfo
}

func brewCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_INSTALL_CLEANUP=1", "HOMEBREW_NO_AUTOREMOVE=1", "HOMEBREW_NO_ANALYTICS=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return append(out, stderr.Bytes()...), err
	}
	return out, nil
}

func detectInstallation(ctx context.Context, binary, invoked string) (uninstallCLI, *Failure) {
	cli := uninstallCLI{State: "pending", Method: "direct"}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil || filepath.Base(real) != "workbox" {
		return cli, fail("UNINSTALL_UNSUPPORTED", "Could not identify a workbox executable")
	}
	cli.Binary, err = filepath.Abs(real)
	if err != nil {
		return cli, ioFailure("Could not resolve the running executable")
	}
	cli.info, err = os.Lstat(cli.Binary)
	if err != nil || !cli.info.Mode().IsRegular() {
		return cli, fail("UNINSTALL_UNSUPPORTED", "Executable is not a regular file")
	}
	// A managed keg must go through its package manager; never unlink it directly.
	keg := filepath.Dir(filepath.Dir(cli.Binary))
	rack := filepath.Dir(keg)
	cellar := filepath.Dir(rack)
	if filepath.Base(cellar) == "Cellar" {
		var receipt struct {
			Source struct {
				Tap string `json:"tap"`
			} `json:"source"`
		}
		body, err := os.ReadFile(filepath.Join(keg, "INSTALL_RECEIPT.json"))
		if err != nil || json.Unmarshal(body, &receipt) != nil || receipt.Source.Tap != "maybell/tap" || filepath.Base(rack) != "workbox" || filepath.Base(filepath.Dir(cli.Binary)) != "bin" {
			return cli, fail("UNINSTALL_UNSUPPORTED", "Homebrew installation ownership could not be verified; use its package manager directly")
		}
		cli.brew = filepath.Join(filepath.Dir(cellar), "bin", "brew")
		if _, err := os.Stat(cli.brew); err != nil {
			cli.brew, err = exec.LookPath("brew")
			if err != nil {
				return cli, fail("UNINSTALL_UNSUPPORTED", "Homebrew is required to uninstall this installation")
			}
		}
		out, err := brewCommand(ctx, cli.brew, "--cellar", "workbox")
		actual, pathErr := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
		if err != nil || pathErr != nil || actual != rack {
			return cli, fail("UNINSTALL_UNSUPPORTED", "Homebrew's Cellar does not match the running executable")
		}
		cli.Method, cli.rack = "homebrew", rack
		return cli, nil
	}
	if path, err := exec.LookPath(invoked); err == nil {
		path, _ = filepath.Abs(path)
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if target, err := filepath.EvalSymlinks(path); err == nil && target == cli.Binary {
				cli.Link, cli.linkInfo = path, info
			}
		}
	}
	return cli, nil
}

func skillCandidates(extra string) (map[string]map[string]string, string, []receiptFile, *Failure) {
	state, e := installStateDir()
	if e != nil {
		return nil, "", nil, e
	}
	candidates := map[string]map[string]string{}
	records := []receiptFile{}
	root, err := os.OpenRoot(state)
	if err == nil {
		defer root.Close()
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			return nil, state, nil, ioFailure("Could not read installation records")
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "skill-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			info, err := root.Lstat(name)
			if err != nil || !info.Mode().IsRegular() {
				return nil, state, nil, invalid("Skill installation record is not a regular file; nothing was uninstalled")
			}
			body, err := root.ReadFile(name)
			var receipt skillReceipt
			if !entry.Type().IsRegular() || err != nil || json.Unmarshal(body, &receipt) != nil || !validReceipt(receipt) || receiptName(receipt.Path) != name {
				return nil, state, nil, invalid("Invalid skill installation record; nothing was uninstalled")
			}
			candidates[receipt.Path] = receipt.Files
			records = append(records, receiptFile{name, body, info})
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, state, nil, ioFailure("Could not open installation records")
	}
	files, err := skillBundle()
	if err != nil {
		return nil, state, nil, ioFailure("Could not read bundled skill")
	}
	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		for _, agent := range []string{".agents", ".codex", ".claude"} {
			dirs = append(dirs, filepath.Join(home, agent, "skills"))
		}
	}
	if extra != "" {
		path, e := absolute(extra)
		if e != nil {
			return nil, state, nil, e
		}
		dirs = append(dirs, path)
	}
	for _, dir := range dirs {
		// Canonicalize the parent only: a linked skill itself must be rejected.
		parent, err := filepath.EvalSymlinks(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, state, nil, ioFailure("Could not inspect a skills directory")
		}
		path := filepath.Join(parent, "workbox")
		if _, known := candidates[path]; !known {
			candidates[path] = skillHashes(files)
		}
	}
	return candidates, state, records, nil
}

func inspectRemoval(path string, files map[string]string) (*uninstallSkill, *Failure) {
	skill := &uninstallSkill{Path: path, State: "unknown", files: files}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		skill.State = "absent"
		return skill, nil
	}
	if err != nil {
		return skill, ioFailure("Could not inspect an installed skill")
	}
	conflict := fail("SKILL_CONFLICT", "A workbox skill was changed or contains unrecognized files; back it up and move it aside before uninstalling")
	conflict.Details["path"] = path
	skill.State = "conflict"
	if !info.IsDir() {
		return skill, conflict
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return skill, ioFailure("Could not open an installed skill")
	}
	defer root.Close()
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) || !sameSkillHashes(root, files) {
		return skill, conflict
	}
	skill.State, skill.info = "pending", info
	return skill, nil
}

func removeSkill(ctx context.Context, skill *uninstallSkill) *Failure {
	if skill.State == "absent" {
		return nil
	}
	current, e := inspectRemoval(skill.Path, skill.files)
	if e != nil {
		return e
	}
	if current.State == "absent" || !os.SameFile(current.info, skill.info) {
		return fail("SKILL_CONFLICT", "Skill changed during uninstall")
	}
	root, err := os.OpenRoot(skill.Path)
	if err != nil {
		return ioFailure("Could not open skill for removal")
	}
	defer root.Close()
	if actual, err := root.Stat("."); err != nil || !os.SameFile(actual, skill.info) {
		return fail("SKILL_CONFLICT", "Skill changed during uninstall")
	}
	dirs := map[string]bool{}
	for _, name := range names(skill.files) {
		if ctx.Err() != nil {
			return fail("INTERRUPTED", "Operation interrupted")
		}
		info, err := root.Lstat(name)
		body, readErr := root.ReadFile(name)
		if err != nil || readErr != nil || !info.Mode().IsRegular() || fmt.Sprintf("%x", sha256.Sum256(body)) != skill.files[name] {
			return fail("SKILL_CONFLICT", "Skill file changed during uninstall")
		}
		if err := root.Remove(name); err != nil {
			return ioFailure("Could not remove a skill file")
		}
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			dirs[dir] = true
		}
	}
	ordered := names(dirs)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, dir := range ordered {
		if err := root.Remove(dir); err != nil {
			return ioFailure("Skill directory is no longer empty; retained remaining files")
		}
	}
	parent, err := os.OpenRoot(filepath.Dir(skill.Path))
	if err != nil {
		return ioFailure("Could not open skill parent for removal")
	}
	defer parent.Close()
	info, err := parent.Lstat("workbox")
	if err != nil || !os.SameFile(info, skill.info) {
		return fail("SKILL_CONFLICT", "Skill root changed during uninstall")
	}
	if err := parent.Remove("workbox"); err != nil {
		return ioFailure("Skill is no longer empty; retained remaining files")
	}
	skill.State = "removed"
	return nil
}

func removeReceipts(state string, records []receiptFile) *Failure {
	if len(records) == 0 {
		return nil
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return ioFailure("Could not open installation records for cleanup")
	}
	defer root.Close()
	for _, record := range records {
		info, err := root.Lstat(record.name)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, record.info) {
			return fail("CONFIG_CONFLICT", "Installation records changed during uninstall")
		}
		body, err := root.ReadFile(record.name)
		if err != nil || !bytes.Equal(body, record.body) {
			return fail("CONFIG_CONFLICT", "Installation records changed during uninstall")
		}
		if err := root.Remove(record.name); err != nil {
			return ioFailure("Could not remove an installation record")
		}
	}
	// Nonrecursive removal preserves any unrelated state files.
	_ = os.Remove(state)
	return nil
}

func removeCLI(ctx context.Context, cli *uninstallCLI) *Failure {
	info, err := os.Lstat(cli.Binary)
	if err != nil || !os.SameFile(info, cli.info) {
		return fail("UNINSTALL_UNSUPPORTED", "Executable changed during uninstall")
	}
	if cli.Method == "homebrew" {
		out, err := brewCommand(ctx, cli.brew, "uninstall", "--formula", "--force", "maybell/tap/workbox")
		if err != nil {
			e := fail("UNINSTALL_FAILED", "Homebrew could not uninstall workbox; skills already removed are reported in data")
			if len(out) > 4096 {
				out = out[len(out)-4096:]
			}
			e.Details["output"] = strings.TrimSpace(string(out))
			return e
		}
		entries, err := os.ReadDir(cli.rack)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || len(entries) != 0) {
			return fail("UNINSTALL_FAILED", "Homebrew left installed workbox versions behind")
		}
	} else {
		parent, err := os.OpenRoot(filepath.Dir(cli.Binary))
		if err != nil {
			return ioFailure("Could not open the executable directory")
		}
		defer parent.Close()
		if current, err := parent.Lstat("workbox"); err != nil || !os.SameFile(current, cli.info) {
			return fail("UNINSTALL_UNSUPPORTED", "Executable changed during uninstall")
		}
		if err := parent.Remove("workbox"); err != nil {
			return ioFailure("Could not remove the executable; check directory permissions")
		}
		if cli.Link != "" {
			info, err := os.Lstat(cli.Link)
			if err != nil || !os.SameFile(info, cli.linkInfo) {
				return fail("UNINSTALL_FAILED", "Invocation link changed; it was retained")
			}
			if err := os.Remove(cli.Link); err != nil {
				return ioFailure("Could not remove the invocation link")
			}
		}
	}
	if _, err := os.Lstat(cli.Binary); !errors.Is(err, os.ErrNotExist) {
		return fail("UNINSTALL_FAILED", "Executable still exists after uninstall")
	}
	cli.State = "removed"
	return nil
}

// All skill destinations are checked before the first deletion. The executable
// is removed last, so a cleanup failure retains a usable CLI for diagnosis/retry.
func uninstall(ctx context.Context, binary, invoked string, flags map[string]string) (data any, failure *Failure) {
	result := &uninstallResult{State: "planned", Skills: []*uninstallSkill{}, Configuration: "preserved", Workspaces: "preserved"}
	defer func() {
		if failure != nil {
			failure.Data = result
			result.State = "failed"
		}
	}()
	if extra, ok := flags["skill-path"]; ok && strings.TrimSpace(extra) == "" {
		return nil, fail("PATH_INVALID", "--skill-path requires a skills parent directory")
	}
	cli, e := detectInstallation(ctx, binary, invoked)
	result.CLI = cli
	if e != nil {
		return nil, e
	}
	candidates, state, records, e := skillCandidates(flags["skill-path"])
	if e != nil {
		return nil, e
	}
	for _, path := range names(candidates) {
		skill, e := inspectRemoval(path, candidates[path])
		result.Skills = append(result.Skills, skill)
		if e != nil {
			return nil, e
		}
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Operation interrupted")
	}
	if flags["dry-run"] == "true" {
		return result, nil
	}
	result.State = "uninstalling"
	for _, skill := range result.Skills {
		if e := removeSkill(ctx, skill); e != nil {
			return nil, e
		}
	}
	if e := removeReceipts(state, records); e != nil {
		return nil, e
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Operation interrupted")
	}
	if e := removeCLI(ctx, &result.CLI); e != nil {
		return nil, e
	}
	result.State = "uninstalled"
	return result, nil
}
