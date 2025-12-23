package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

func FS() fs.FS {
	return files
}
