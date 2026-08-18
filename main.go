// Command graf queries a Graf deployment's unified data graph from the
// command line.
package main

import (
	"os"

	"github.com/nabooai/grafcli/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
