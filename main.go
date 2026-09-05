package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.1.3"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) < 2 {
		usage()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	switch os.Args[1] {
	case "server":
		fs := flag.NewFlagSet("server", flag.ExitOnError)
		config := fs.String("config", defaultServerConfig(), "server config path")
		_ = fs.Parse(os.Args[2:])
		err = runServer(ctx, *config)
	case "agent":
		fs := flag.NewFlagSet("agent", flag.ExitOnError)
		config := fs.String("config", defaultAgentConfig(), "agent config path")
		once := fs.Bool("once", false, "scan and upload once")
		_ = fs.Parse(os.Args[2:])
		err = runAgent(ctx, *config, *once, false)
	case "backfill":
		fs := flag.NewFlagSet("backfill", flag.ExitOnError)
		config := fs.String("config", defaultAgentConfig(), "agent config path")
		_ = fs.Parse(os.Args[2:])
		err = runAgent(ctx, *config, true, true)
	case "enroll":
		err = enrollCommand(os.Args[2:])
	case "backup":
		err = backupCommand(os.Args[2:])
	case "restore":
		err = restoreCommand(os.Args[2:])
	case "hash-password":
		err = hashPasswordCommand(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Println(version)
		return
	default:
		usage()
	}
	if err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: codex-meter <server|agent|backfill|enroll|backup|restore|hash-password|version>")
	os.Exit(2)
}
