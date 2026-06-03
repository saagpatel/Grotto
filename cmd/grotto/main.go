// Command grotto renders OpenTelemetry trace waterfalls for shell commands,
// build scripts, and test suites — entirely local, persisting to SQLite.
package main

import (
	"os"

	"github.com/saagpatel/grotto/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
