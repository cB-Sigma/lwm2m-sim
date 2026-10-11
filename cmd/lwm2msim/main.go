// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command lwm2msim runs a simulated LwM2M client: it registers with an
// LwM2M server (or bootstraps first) and serves the Server, Device,
// Connectivity Monitoring and Temperature objects, with a temperature
// that drifts so that observations get notifications.
//
//	lwm2msim -server coap://127.0.0.1:5683 -ep demo-1
//	lwm2msim -server coaps://127.0.0.1:5684 -ep demo-1 -psk-id demo-1 -psk 000102030405060708090a0b0c0d0e0f
//	lwm2msim -bootstrap coap://127.0.0.1:5783 -ep demo-1
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cB-Sigma/lwm2m-sim/sim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lwm2msim:", err)
		os.Exit(1)
	}
}

// run parses args, starts the client and keeps it running until ctx ends.
func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("lwm2msim", flag.ContinueOnError)
	fs.SetOutput(out)
	server := fs.String("server", "", "LwM2M server URI, coap://host:port or coaps://host:port")
	bootstrap := fs.String("bootstrap", "", "bootstrap server URI (bootstrap first; -server is then not used)")
	ep := fs.String("ep", "lwm2m-sim", "endpoint name")
	pskID := fs.String("psk-id", "", "DTLS PSK identity (coaps)")
	pskHex := fs.String("psk", "", "DTLS pre-shared key in hex (coaps)")
	lifetime := fs.Duration("lifetime", 300*time.Second, "registration lifetime")
	version := fs.String("version", "1.1", "LwM2M version to register with: 1.0, 1.1 or 1.2")
	interval := fs.Duration("interval", 5*time.Second, "how often the temperature changes (0: never)")
	verbose := fs.Bool("v", false, "log every request")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" && *bootstrap == "" {
		return errors.New("give -server or -bootstrap")
	}
	key, err := hex.DecodeString(*pskHex)
	if err != nil {
		return fmt.Errorf("-psk is not hex: %w", err)
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	c := sim.New(sim.Config{Endpoint: *ep, Server: *server, Bootstrap: *bootstrap, PSKIdentity: *pskID, PSK: key,
		Lifetime: *lifetime, Version: *version, Logger: slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level}))})
	if err := c.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Stop() }()
	var tick <-chan time.Time
	if *interval > 0 {
		t := time.NewTicker(*interval)
		defer t.Stop()
		tick = t.C
	}
	for step := 0; ; step++ {
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
			// A slow made-up wave between 19.5 and 23.5 °C.
			v := math.Round((21.5+2*math.Sin(float64(step)/6))*10) / 10
			_ = c.Set("/3303/0/5700", v)
		}
	}
}
