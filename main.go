// Command agi is the entry point for the agi CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/ChrisVilches/agi/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// interruptStatus is the exit status of a run the user stopped. 128 plus the
// signal's number is what a shell reports for a process killed by one, and
// SIGINT is 2; a run that answered nothing because it was interrupted should
// not be indistinguishable from one that failed.
const interruptStatus = 130

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The first Ctrl-C cancels the run's context, which is how it reaches
	// everything the run is waiting on: the request in flight, the command a
	// tool is running. The run then ends the way it ends anyway — the
	// conversation is stored, the log is closed — rather than being killed
	// where it stands.
	//
	// Handling is given back to the operating system as soon as that starts,
	// so a second Ctrl-C kills agi outright. Shutting down gracefully is worth
	// waiting for once; a user who finds themselves waiting for it twice
	// should not have to reach for another terminal to stop the program.
	go func() {
		<-ctx.Done()
		stop()
	}()

	err := cli.NewApp(version).Run(ctx, os.Args)
	if err == nil {
		return
	}
	// Every error is printed the same way and with nothing in front of it. Not
	// the program's name: whoever ran agi knows what they ran, and a run the
	// model ended because the request could not be answered reports the
	// model's own sentence about it, which agi would be passing off as its own.
	//
	// The status is the only thing that varies, and only for an interruption,
	// which a shell can tell from a failure. Nothing else earns a case here: an
	// exit status says that a run failed, not why, and nothing downstream of
	// agi could tell an unanswerable prompt from a gateway it could not reach.
	fmt.Fprintln(os.Stderr, err)
	if errors.Is(err, cli.ErrInterrupted) {
		os.Exit(interruptStatus)
	}
	os.Exit(1)
}
