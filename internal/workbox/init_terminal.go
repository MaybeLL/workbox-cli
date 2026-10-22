package workbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/sys/unix"
)

func terminalInteractive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	_, a := terminalState(int(os.Stdin.Fd()))
	_, b := terminalState(int(f.Fd()))
	return a == nil && b == nil
}

// Poll before reading avoids a background reader surviving cancellation. The
// terminal remains canonical; passwords change echo only and restore on return.
func terminalLine(ctx context.Context, w io.Writer, label, defaultValue string, secret bool) (string, *Failure) {
	fmt.Fprint(w, label)
	if defaultValue != "" {
		fmt.Fprintf(w, " [%s]", defaultValue)
	}
	fmt.Fprint(w, ": ")
	fd := int(os.Stdin.Fd())
	if secret {
		old, err := terminalState(fd)
		if err != nil {
			return "", ioFailure("Could not access terminal")
		}
		next := *old
		next.Lflag &^= unix.ECHO
		if err = terminalSet(fd, &next); err != nil {
			return "", ioFailure("Could not hide password input")
		}
		defer func() { _ = terminalSet(fd, old); fmt.Fprintln(w) }()
	}
	buf := []byte{}
	one := make([]byte, 1)
	for {
		if ctx.Err() != nil {
			return "", fail("INTERRUPTED", "Initialization interrupted")
		}
		p := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(p, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return "", ioFailure("Could not read terminal")
		}
		if n == 0 {
			continue
		}
		n, err = unix.Read(fd, one)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return "", ioFailure("Could not read terminal")
		}
		if n == 0 {
			return "", fail("INTERRUPTED", "Initialization cancelled")
		}
		if one[0] == '\n' {
			s := strings.TrimSuffix(string(buf), "\r")
			if s == "" {
				s = defaultValue
			}
			return s, nil
		}
		buf = append(buf, one[0])
		if len(buf) > 1024*1024 {
			return "", fail("INVALID_ARGUMENTS", "Input is too long")
		}
	}
}
func terminalYes(ctx context.Context, w io.Writer, label string, defaultYes bool) (bool, *Failure) {
	def := "n"
	if defaultYes {
		def = "y"
	}
	for {
		s, e := terminalLine(ctx, w, label+" (y/n)", def, false)
		if e != nil {
			return false, e
		}
		switch strings.ToLower(s) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(w, "Enter y or n.")
	}
}
func (a *app) interactiveInit(ctx context.Context, root string, old settingsRecord, w io.Writer) (*initPlan, *Failure) {
	fmt.Fprintln(w, "Workbox initialization\nConfiguration directory: "+root)
	fmt.Fprintf(w, "Environment overrides: %v\n", overrideNames())
	source, sourceErr := sourceStore(root, old)
	if sourceErr == nil {
		human(w, map[string]any{"current_storage": storeIdentity(source)}, 0)
	} else {
		fmt.Fprintln(w, "Current connection is unreadable; provide a new connection and explicitly decline copying to repair it.")
	}
	for {
		def := "1"
		if _, ok := source.(*mysqlStore); ok {
			def = "2"
		}
		choice, e := terminalLine(ctx, w, "Storage: 1 SQLite / 2 MySQL / 0 cancel", def, false)
		if e != nil {
			return nil, e
		}
		if choice == "0" {
			return nil, fail("INTERRUPTED", "Initialization cancelled")
		}
		if choice != "1" && choice != "2" {
			continue
		}
		o := initOptions{backend: "sqlite"}
		var target configStore = &sqliteStore{path: root + string(os.PathSeparator) + "config.db"}
		if choice == "2" {
			o.backend = "mysql"
			mode, e := terminalLine(ctx, w, "MySQL: 1 saved/environment / 2 DSN file / 3 connection form / 0 back", "1", false)
			if e != nil {
				return nil, e
			}
			if mode == "0" {
				continue
			}
			switch mode {
			case "1":
				target, e = candidateStore(root, old, o)
			case "2":
				o.dsnFile, e = terminalLine(ctx, w, "DSN input file", "", false)
				if e == nil {
					target, e = candidateStore(root, old, o)
				}
			case "3":
				cfg := mysql.NewConfig()
				cfg.Net = "tcp"
				cfg.TLSConfig = "true"
				host, err := terminalLine(ctx, w, "Host", "localhost", false)
				if err != nil {
					return nil, err
				}
				port, err := terminalLine(ctx, w, "Port", "3306", false)
				if err != nil {
					return nil, err
				}
				number, parseErr := strconv.Atoi(port)
				if parseErr != nil || number < 1 || number > 65535 {
					fmt.Fprintln(w, "Port must be between 1 and 65535.")
					continue
				}
				cfg.Addr = net.JoinHostPort(host, port)
				cfg.DBName, e = terminalLine(ctx, w, "Existing dedicated database", "", false)
				if e != nil {
					return nil, e
				}
				cfg.User, e = terminalLine(ctx, w, "User", "", false)
				if e != nil {
					return nil, e
				}
				cfg.Passwd, e = terminalLine(ctx, w, "Password", "", true)
				if e != nil {
					return nil, e
				}
				tls, e := terminalYes(ctx, w, "Enable verified TLS", true)
				if e != nil {
					return nil, e
				}
				if !tls {
					cfg.TLSConfig = "false"
				}
				target, e = newMySQLStore(cfg.FormatDSN())
			default:
				continue
			}
			if e != nil {
				fmt.Fprintln(w, e.Message)
				continue
			}
		}
		if e := checkInitEnvironment(target); e != nil {
			return nil, e
		}
		if _, e := target.snapshot(ctx); e != nil {
			if ctx.Err() != nil {
				return nil, e
			}
			fmt.Fprintln(w, e.Message)
			continue
		}
		if sourceErr != nil || !sameStore(source, target) {
			o.copy, e = terminalYes(ctx, w, "Copy existing repository/workspace configurations (retains the source)", false)
			if e != nil {
				return nil, e
			}
			o.noCopy = !o.copy
		}
		defaultInstall := true
		if old.value != nil {
			defaultInstall = old.value.Skill.Install
		}
		o.install, e = terminalYes(ctx, w, "Install companion workbox Skill", defaultInstall)
		if e != nil {
			return nil, e
		}
		o.skip = !o.install
		if o.install {
			parent, e := skillParent(old, "")
			if e != nil {
				return nil, e
			}
			o.skillPath, e = terminalLine(ctx, w, "Skills parent directory", parent, false)
			if e != nil {
				return nil, e
			}
			parent, e = absolute(o.skillPath)
			if e != nil {
				return nil, e
			}
			// Read-only conflict preview. A race after confirmation is still handled by the installer.
			if handle, err := os.OpenRoot(parent); err == nil {
				files, err := skillBundle()
				if err != nil {
					handle.Close()
					return nil, ioFailure("Could not read bundled Skill")
				}
				_, e = existingSkill(handle, files)
				handle.Close()
				if e != nil {
					fmt.Fprintln(w, e.Message)
					continue
				}
			}
		}
		plan, e := planInit(ctx, root, old, o, target)
		if e != nil {
			if ctx.Err() != nil {
				return nil, e
			}
			fmt.Fprintln(w, e.Message)
			continue
		}
		initSummary(w, plan)
		action, e := terminalLine(ctx, w, "1 execute / 2 back and edit / 0 cancel", "1", false)
		if e != nil {
			return nil, e
		}
		switch action {
		case "1":
			return plan, nil
		case "0":
			return nil, fail("INTERRUPTED", "Initialization cancelled")
		}
	}
}
