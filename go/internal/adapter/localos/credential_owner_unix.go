//go:build darwin || linux

package localos

import (
	"os"
	"syscall"
)

func credentialFileOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
