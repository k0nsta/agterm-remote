// Package remotescript contains the remote agr script shipped by the Mac
// binary.
package remotescript

import (
	"bytes"
	_ "embed"
)

//go:embed agr.sh
var script []byte

// Script returns the remote script with its build version embedded.
func Script(version string) []byte {
	return bytes.ReplaceAll(script, []byte("@VERSION@"), []byte(version))
}
