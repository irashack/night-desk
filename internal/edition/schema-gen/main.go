// Command schema-gen updates both published schemas; run with go generate ./internal/edition.
// Optional arguments select the v1 and v2 output paths for CI comparisons.
package main

import (
	"os"
	"path/filepath"

	"github.com/irashack/night-desk/internal/edition"
)

func main() {
	paths := []string{"../../schema/edition-v1.json", "../../schema/edition-v2.json"}
	if len(os.Args) > 1 {
		paths[0] = os.Args[1]
		paths[1] = filepath.Join(filepath.Dir(paths[0]), "edition-v2.json")
	}
	if len(os.Args) > 2 {
		paths[1] = os.Args[2]
	}
	for i, out := range paths {
		data, err := edition.JSONSchemaVersion(i + 1)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(out, append(data, '\n'), 0644); err != nil {
			panic(err)
		}
	}
}
