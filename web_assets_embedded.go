//go:build !api_only

package main

import "embed"

//go:embed web/dist
var buildFS embed.FS

//go:embed web/dist/index.html
var indexPage []byte

const apiOnly = false
