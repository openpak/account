// Package migrations embeds the SQL migration files.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

// MigrationsFS exposes the embedded migration files (tests, tooling).
func MigrationsFS() embed.FS { return FS }
