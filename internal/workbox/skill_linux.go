package workbox

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishSkill(parent *os.Root, stage string) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return unix.Renameat2(int(dir.Fd()), stage, int(dir.Fd()), "workbox", unix.RENAME_NOREPLACE)
}
