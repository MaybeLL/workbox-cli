package workbox

import "golang.org/x/sys/unix"

func terminalState(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TCGETS) }
func terminalSet(fd int, t *unix.Termios) error   { return unix.IoctlSetTermios(fd, unix.TCSETS, t) }
