// Package stackassets embeds the version policy and example decoy assets for
// standalone binaries. The JSON policy remains the source of pinned images.
package stackassets

import "embed"

//go:embed compatibility.json
var compatibility []byte

func Compatibility() []byte { return append([]byte(nil), compatibility...) }

//go:embed decoy/nginx.conf decoy/site.example/*
var decoy embed.FS

// Decoy reads only the bundled example site and nginx configuration.
func Decoy(name string) ([]byte, error) { return decoy.ReadFile(name) }
