//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package vault

import (
	"context"
	"fmt"
)

func acquireVaultFileLock(context.Context, string) (func() error, error) {
	return nil, fmt.Errorf("%w: TaskStore file locking is unsupported on this platform", ErrInvalidInput)
}
