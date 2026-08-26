//go:build !darwin && !linux

package localos

import "os"

func credentialFileOwnedByCurrentUser(os.FileInfo) bool { return false }
