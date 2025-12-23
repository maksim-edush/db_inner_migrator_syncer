package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/*.tmpl templates/partials/*.tmpl static/*
var assets embed.FS

func TemplatesFS() fs.FS {
	sub, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

func StaticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
