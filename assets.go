package demo

import "embed"

// Assets 随二进制发布；Swagger UI 使用本地固定版本资源。
//
//go:embed web openapi/user.json openapi/admin.json README.md
var Assets embed.FS
