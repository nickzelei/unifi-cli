// Command unifi is a CLI for the UniFi Network integration API.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/nickzelei/unifi-cli/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], cli.StdIO())
	stop()
	os.Exit(code)
}
