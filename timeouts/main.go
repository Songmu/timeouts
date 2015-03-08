package main

import (
	"fmt"
	"syscall"

	"github.com/Songmu/timeouts"
)

func main() {
	tio := &timeouts.Timeouts{
		Command: "test/countup.p",
		Signal:  syscall.SIGTERM,
	}
	exit := tio.Run()

	fmt.Printf("command exited with: %d\n", exit)
}
