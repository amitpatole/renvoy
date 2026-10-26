// Command renvoy compiles a provider-neutral pipeline into native CI/CD configuration.
package main

import (
	"fmt"
	"os"

	"github.com/amitpatole/renvoy/internal/app"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(app.ExitFailure)
	}
	os.Exit(app.Run(os.Args[1:], os.Stdout, os.Stderr, dir))
}
