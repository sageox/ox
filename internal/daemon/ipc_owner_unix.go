//go:build !windows

package daemon

import "os"

func currentProcessUID() uint32 { return uint32(os.Geteuid()) }
