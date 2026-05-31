//go:build darwin || linux || freebsd || openbsd

package health

import "syscall"

type syscallStatfs = syscall.Statfs_t

var statfsFunc = syscall.Statfs
