// Command lanatt is both the root daemon and the OCI hook that calls it.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lanatt/internal/config"
	"lanatt/internal/daemon"
	"lanatt/internal/hook"
	"lanatt/internal/runtime"
	"lanatt/internal/wire"
)

const usage = `usage:
  lanatt daemon [-config /etc/lanatt/config.json]
  lanatt hook create|stop [-socket /run/lanatt/lanatt.sock]`

func main() {
	if runtime.IsWrapper(os.Args[0]) {
		fail(filepath.Base(os.Args[0]) + ": " + runRuntime(os.Args[0], os.Args[1:]).Error())
	}
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

// runRuntime is the Docker path: Docker has no hook directory, so lanatt-<runtime> adds the
// hooks to an annotated bundle and then becomes that runtime. It only returns on failure.
func runRuntime(argv0 string, args []string) error {
	target, err := runtime.Target(argv0, os.Getenv("LANATT_RUNTIME"))
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if bundle := runtime.Bundle(args, cwd); bundle != "" {
		if err := injectHooks(filepath.Join(bundle, "config.json")); err != nil {
			return err
		}
	}
	bin, err := exec.LookPath(target)
	if err != nil {
		return err
	}
	return syscall.Exec(bin, append([]string{filepath.Base(target)}, args...), os.Environ())
}

func injectHooks(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	socket := os.Getenv("LANATT_SOCKET")
	if socket == "" {
		socket = wire.DefaultSocket
	}
	// The hook must run as the lanatt command, not as the wrapper.
	out, changed, err := runtime.InjectHooks(data, filepath.Join(filepath.Dir(self), "lanatt"), socket)
	if err != nil || !changed {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, fi.Mode())
}
