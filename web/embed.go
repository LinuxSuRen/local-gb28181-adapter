// Package web 内嵌前端构建产物（web/dist），
// 使后端单二进制即可同时提供管理 UI。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist 返回前端静态文件系统（根即 dist 目录）。
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
