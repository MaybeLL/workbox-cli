package workbox

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishDir(parent *os.Root, stage, target string) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Renameat2(int(dir.Fd()), stage, int(dir.Fd()), target, unix.RENAME_NOREPLACE)
}
