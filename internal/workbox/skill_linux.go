package workbox

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishDir(parent *os.Root, stage, target string) error {
	return renameDirs(parent, stage, target, unix.RENAME_NOREPLACE)
}
func exchangeDirs(parent *os.Root, stage, target string) error {
	return renameDirs(parent, stage, target, unix.RENAME_EXCHANGE)
}
func renameDirs(parent *os.Root, stage, target string, flags uint) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Renameat2(int(dir.Fd()), stage, int(dir.Fd()), target, flags)
}
