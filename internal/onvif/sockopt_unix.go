//go:build !windows

package onvif

import "syscall"

// setReuseAddr 在监听 socket 上打开 SO_REUSEADDR（POSIX 系 fd 为 int）。
func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
