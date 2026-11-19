// Package macroconfig contains the reviewed, device-independent Klipper bundle.
package macroconfig

import _ "embed"

// Filament is the fresh-printer configuration offered by PCMA.
//
//go:embed pcma-filament.cfg
var Filament []byte
