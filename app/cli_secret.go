package app

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
)

func readSecret(cio cliIO, name string) (string, error) {
	if f, ok := cio.in.(*os.File); ok && cio.tty {
		fmt.Fprintf(cio.err, "%s: ", name)
		restore, err := noEcho(f)
		if err == nil {
			// Ctrl-C would otherwise exit with echo still off.
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt)
			done := make(chan struct{})
			go func() {
				select {
				case <-sig:
					restore()
					fmt.Fprintln(cio.err)
					os.Exit(ExitInterrupted)
				case <-done:
				}
			}()
			defer func() {
				close(done)
				signal.Stop(sig)
				restore()
				fmt.Fprintln(cio.err)
			}()
		}
	}
	line, err := bufio.NewReader(cio.in).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		if err != nil {
			return "", errors.New("nothing on stdin")
		}
		return "", errors.New("empty")
	}
	return line, nil
}
