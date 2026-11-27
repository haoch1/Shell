// Package frontend exposes the application version declared in the frontend manifest.
package frontend

import (
	_ "embed"
	"encoding/json"
)

//go:embed package.json
var manifest []byte

func Version() string {
	var pkg struct{ Version string }
	if err := json.Unmarshal(manifest, &pkg); err != nil || pkg.Version == "" {
		panic("missing application version in frontend/package.json")
	}
	return pkg.Version
}
