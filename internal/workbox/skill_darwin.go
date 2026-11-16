package workbox

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishDir(parent *os.Root, stage, target string) error {
	return renameDirs(parent, stage, target, unix.RENAME_EXCL)
}
func exchangeDirs(parent *os.Root, stage, target string) error {
	return renameDirs(parent, stage, target, unix.RENAME_SWAP)
}
func renameDirs(parent *os.Root, stage, target string, flags uint32) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.RenameatxNp(int(dir.Fd()), stage, int(dir.Fd()), target, flags)
}
