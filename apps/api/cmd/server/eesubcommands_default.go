//go:build !ee

package main

// runEESubcommands is the OSS no-op for Enterprise-only one-shot subcommands
// (e.g. the BoltDB→Postgres data migration). The Enterprise build (`-tags ee`)
// overrides this; keeping the seam here means main.go stays identical between
// OSS and EE.
func runEESubcommands() {}
