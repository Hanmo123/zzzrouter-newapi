//go:build api_only

package main

import "embed"

// API-only builds have no dependency on web/dist. The independent frontend
// server owns static files and proxies API requests to this backend.
var buildFS embed.FS
var indexPage []byte

const apiOnly = true
