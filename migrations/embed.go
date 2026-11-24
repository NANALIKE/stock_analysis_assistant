// Package migrations holds the forward-only schema shipped with each binary.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
