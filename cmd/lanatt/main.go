// Command lanatt is both the root daemon and the OCI hook that calls it.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lanatt/internal/config"
	"lanatt/internal/daemon"
	"lanatt/internal/hook"
	"lanatt/internal/wire"
)

const usage = `usage:
  lanatt daemon [-config /etc/lanatt/config.json]
  lanatt hook create|stop [-socket /run/lanatt.sock]`

func main() {
	if len(os.Args) < 2 {
		fail(usage)
	}
	var err error
	switch os.Args[1] {
	case "daemon":
		err = runDaemon(os.Args[2:])
	case "hook":
		err = runHook(os.Args[2:])
	default:
		fail(usage)
	}
	if err != nil {
		fail("lanatt: " + err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	path := fs.String("config", "/etc/lanatt/config.json", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	srv, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	ln, err := srv.Listen()
	if err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		ln.Close()
	}()
	log.Printf("listening on %s", cfg.Socket)
	return srv.Serve(ln)
}

func runHook(args []string) error {
	if len(args) < 1 {
		return hook.ErrUsage
	}
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	socket := fs.String("socket", wire.DefaultSocket, "daemon socket")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return hook.Run(args[0], os.Stdin, func(req wire.Request) error {
		return wire.Call(*socket, req, 30*time.Second)
	})
}
