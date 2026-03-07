//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

func init() {
	signalNotifyFunc = func(ch chan<- os.Signal) {
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	}
}
