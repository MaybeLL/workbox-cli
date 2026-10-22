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
	fmt.Fprint(w, label)
	if defaultValue != "" {
		fmt.Fprintf(w, " [%s]", defaultValue)
	}
	fmt.Fprint(w, ": ")
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
	selected := 1
	if defaultYes {
		selected = 0
	}
	value, e := terminalSelect(ctx, w, label, []string{"Yes", "No"}, selected)
	return value == 0, e
}
func (a *app) interactiveInit(ctx context.Context, root string, old settingsRecord, w io.Writer) (*initPlan, *Failure) {
	fmt.Fprintln(w, "Workbox initialization\nConfiguration directory: "+root)
	if overrides := overrideNames(); len(overrides) > 0 {
		fmt.Fprintf(w, "Environment overrides: %s\n", strings.Join(overrides, ", "))
	}
	source, sourceErr := sourceStore(root, old)
	if sourceErr == nil {
		fmt.Fprintln(w, "Current storage: "+storageLabel(source))
	} else {
		fmt.Fprintln(w, "Current connection is unreadable; provide a new connection and explicitly decline copying to repair it.")
	}
	def := 0
	if _, ok := source.(*mysqlStore); ok {
		def = 1
	}
	installDefault := true
	if old.value != nil {
		installDefault = old.value.Skill.Install
	}
	rememberedPath := ""
	var rememberedMySQL configStore
	for {
		choice, e := terminalSelect(ctx, w, "Choose configuration storage", []string{"SQLite — local storage", "MySQL — existing database"}, def)
		if e != nil {
			return nil, e
		}
		def = choice
		o := initOptions{backend: "sqlite"}
		var target configStore = &sqliteStore{path: root + string(os.PathSeparator) + "config.db"}
		if choice == 1 {
			o.backend = "mysql"
			mode, e := terminalSelect(ctx, w, "MySQL connection", []string{"Use current / saved connection", "Read a DSN file", "Enter connection details", "Back to storage selection"}, 0)
			if e != nil {
				return nil, e
			}
			if mode == 3 {
				continue
			}
			switch mode {
			case 0:
				if rememberedMySQL != nil {
					target = rememberedMySQL
				} else {
					target, e = candidateStore(root, old, o)
				}
			case 1:
				o.dsnFile, e = terminalLine(ctx, w, "DSN input file", "", false)
				if e == nil {
					target, e = candidateStore(root, old, o)
				}
			case 2:
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
		if choice == 1 {
			rememberedMySQL = target
		}
		if e := checkInitEnvironment(target); e != nil {
			return nil, e
		}
		fmt.Fprintln(w, "Checking storage…")
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
		o.install, e = terminalYes(ctx, w, "Install companion workbox Skill", installDefault)
		if e != nil {
			return nil, e
		}
		installDefault = o.install
		o.skip = !o.install
		if o.install {
			parent, e := skillParent(old, rememberedPath)
			if e != nil {
				return nil, e
			}
			fmt.Fprintln(w, "\nSkill destination: "+parent+string(os.PathSeparator)+"workbox")
			location, choiceErr := terminalSelect(ctx, w, "Where should the Skill be installed?", []string{"Use the displayed directory", "Choose a different directory"}, 0)
			if choiceErr != nil {
				return nil, choiceErr
			}
			o.skillPath = parent
			if location == 1 {
				fmt.Fprintln(w, "Enter a directory path, for example ~/.agents/skills. Press Enter to keep the displayed path.")
				o.skillPath, e = terminalLine(ctx, w, "Skills parent directory", parent, false)
			}
			if e != nil {
				return nil, e
			}
			rememberedPath = o.skillPath
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
		action, e := terminalSelect(ctx, w, "Ready to initialize?", []string{"Initialize Workbox", "Back and edit", "Cancel"}, 0)
		if e != nil {
			return nil, e
		}
		switch action {
		case 0:
			return plan, nil
		case 2:
			return nil, fail("INTERRUPTED", "Initialization cancelled")
		}
	}
}
