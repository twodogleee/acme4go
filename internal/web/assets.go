// Package web 嵌入经过 Vite 构建的管理端静态资源。
//
// @author Andersen
package web

import "embed"

//go:embed dist
var Assets embed.FS
