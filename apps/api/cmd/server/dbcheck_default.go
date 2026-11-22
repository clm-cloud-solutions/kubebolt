//go:build !ee

package main

import "context"

// databaseCheck is /health/db's ping. This edition keeps its stores in
// process (BoltDB), so there is no database to lose: the route answers ok.
func databaseCheck() func(context.Context) error { return nil }
