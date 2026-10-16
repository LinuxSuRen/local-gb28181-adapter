//go:build windows

package onvif

import "syscall"

// setReuseAddr 在监听 socket 上打开 SO_REUSEADDR（Windows 系 fd 为 syscall.Handle），
// 允许与本机其它 WS-Discovery 服务共存于 3702 端口。
func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
