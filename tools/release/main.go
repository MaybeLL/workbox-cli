// Command release builds supported CGO-free binaries, archives and a tap recipe.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

var targets = []string{"darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64"}

func main() {
	version := flag.String("version", "", "release version, without v")
	output := flag.String("output", "dist", "new output directory (must not exist)")
	notices := flag.String("notices-output", "", "write bundled dependency notices only")
	flag.Parse()
	if *notices != "" {
		if err := writeNotices(*notices); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`).MatchString(*version) {
		fmt.Fprintln(os.Stderr, "--version requires a semantic version without v")
		os.Exit(2)
	}
	if err := release(*version, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func release(version, output string) error {
	if err := os.Mkdir(output, 0755); err != nil {
		return fmt.Errorf("output must be a new directory: %w", err)
	}
	work, err := os.MkdirTemp("", "workbox-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	notices := filepath.Join(work, "THIRD_PARTY_NOTICES.txt")
	if err := writeNotices(notices); err != nil {
		return err
	}
	checksums := map[string]string{}
	for _, target := range targets {
		parts := strings.Split(target, "/")
		binary := filepath.Join(work, "workbox")
		fmt.Fprintln(os.Stderr, "Building", target)
		cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-ldflags=-s -w -X main.version="+version, "-o", binary, "./cmd/workbox")
		cmd.Env = environment(os.Environ(), map[string]string{"GOOS": parts[0], "GOARCH": parts[1], "CGO_ENABLED": "0"})
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", target, err)
		}
		name := "workbox_" + version + "_" + parts[0] + "_" + parts[1] + ".tar.gz"
		archive := filepath.Join(output, name)
		if err := pack(archive, binary, notices); err != nil {
			return err
		}
		body, err := os.ReadFile(archive)
		if err != nil {
			return err
		}
		checksums[name] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	names := []string{}
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums bytes.Buffer
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  %s\n", checksums[name], name)
	}
	if err := os.WriteFile(filepath.Join(output, "workbox_"+version+"_checksums.txt"), sums.Bytes(), 0644); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(output, "homebrew"), 0755); err != nil {
		return err
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(output, "homebrew", "workbox.rb"), []byte(formula(version, strings.TrimSpace(string(revision)))), 0644)
}
func environment(env []string, values map[string]string) []string {
	result := []string{}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := values[key]; !replace {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
func pack(path, binary, notices string) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if e := file.Close(); err == nil {
			err = e
		}
	}()
	gz := gzip.NewWriter(file)
	defer func() {
		if e := gz.Close(); err == nil {
			err = e
		}
	}()
	tw := tar.NewWriter(gz)
	defer func() {
		if e := tw.Close(); err == nil {
			err = e
		}
	}()
	files := map[string]string{"workbox": binary, "README.md": "README.md", "LICENSE": "LICENSE", "THIRD_PARTY_NOTICES.txt": notices, "scripts/check-install.sh": "scripts/check-install.sh"}
	for _, directory := range []string{"docs", "skills/workbox"} {
		if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				files[filepath.ToSlash(path)] = path
			}
			return nil
		}); err != nil {
			return err
		}
	}
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		input, err := os.Open(files[name])
		if err != nil {
			return err
		}
		info, err := input.Stat()
		if err != nil {
			input.Close()
			return err
		}
		mode := int64(0644)
		if name == "workbox" {
			mode = 0755
		}
		err = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: info.Size(), Typeflag: tar.TypeReg})
		if err == nil {
			_, err = io.Copy(tw, input)
		}
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func writeNotices(path string) error {
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", "-json", "./cmd/workbox")
	cmd.Env = environment(os.Environ(), map[string]string{"CGO_ENABLED": "0"})
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return err
	}
	type module struct {
		Path, Version, Dir string
		Main               bool
	}
	modules := map[string]module{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p struct{ Module *module }
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if p.Module != nil && !p.Module.Main {
			modules[p.Module.Path] = *p.Module
		}
	}
	var text bytes.Buffer
	text.WriteString("Third-party notices for bundled Go runtime and libraries.\n\nGo runtime\n==========\n")
	license, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if os.IsNotExist(err) { // Homebrew installs LICENSE beside its libexec GOROOT.
		license, err = os.ReadFile(filepath.Join(filepath.Dir(runtime.GOROOT()), "LICENSE"))
	}
	if err != nil {
		return err
	}
	text.Write(license)
	names := []string{}
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m := modules[name]
		entries, err := os.ReadDir(m.Dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			lower := strings.ToLower(entry.Name())
			if entry.IsDir() || (!strings.HasPrefix(lower, "license") && !strings.HasPrefix(lower, "copying") && !strings.HasPrefix(lower, "notice")) {
				continue
			}
			body, err := os.ReadFile(filepath.Join(m.Dir, entry.Name()))
			if err != nil {
				return err
			}
			fmt.Fprintf(&text, "\n\n%s@%s / %s\n==========\n", m.Path, m.Version, entry.Name())
			text.Write(body)
		}
	}
	return os.WriteFile(path, text.Bytes(), 0644)
}
func formula(version, revision string) string {
	return fmt.Sprintf(`class Workbox < Formula
  desc "Reusable configurations and safe local Git workspace preparation"
  homepage "https://github.com/MaybeLL/workbox-cli"
  url "https://github.com/MaybeLL/workbox-cli.git", tag: "v%s", revision: %q
  version %q
  license "MIT"

  depends_on "go" => :build
  uses_from_macos "git"

  def install
    ENV["CGO_ENABLED"] = "0"
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "./cmd/workbox"
    system "go", "run", "./tools/release", "--notices-output", "THIRD_PARTY_NOTICES.txt"
    doc.install "THIRD_PARTY_NOTICES.txt", "docs"
    pkgshare.install "scripts/check-install.sh"
  end

  test do
    system "sh", pkgshare/"check-install.sh", bin/"workbox"
  end
end
`, version, revision, version)
}
