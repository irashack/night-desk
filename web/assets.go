// Package web embeds the reader's complete offline shell.
package web

import "embed"

//go:embed templates/*.html static manifest.webmanifest
var Files embed.FS
