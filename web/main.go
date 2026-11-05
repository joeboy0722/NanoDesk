package main

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var assets embed.FS

func main() {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	runWeb(sub)
}
