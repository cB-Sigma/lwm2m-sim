// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunNeedsAServer(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), nil, &out); err == nil || !strings.Contains(err.Error(), "-server") {
		t.Errorf("run without a server = %v", err)
	}
	if err := run(context.Background(), []string{"-server", "coap://127.0.0.1:1", "-psk", "zz"}, &out); err == nil {
		t.Error("a non-hex key was accepted")
	}
}

// The runner registers with a server that answers 2.01 and stops when
// its context ends.
func TestRunRegisters(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	got := make(chan string, 4)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			m := buf[:n]
			if len(m) < 4 || m[0]>>6 != 1 || m[1] != 2 { // a CON POST
				continue
			}
			got <- string(m)
			// ACK with 2.01 Created and Location-Path "rd", "1".
			tkl := int(m[0] & 0x0f)
			ack := []byte{0x60 | byte(tkl), 0x41, m[2], m[3]}
			ack = append(ack, m[4:4+tkl]...)
			ack = append(ack, 0x82, 'r', 'd', 0x01, '1')
			_, _ = pc.WriteTo(ack, addr)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- run(ctx, []string{"-server", "coap://" + pc.LocalAddr().String(), "-ep", "cli-1", "-interval", "0"}, &out)
	}()
	select {
	case m := <-got:
		if !strings.Contains(m, "ep=cli-1") || !strings.Contains(m, "</3303/0>") {
			t.Errorf("register = %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no register")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("run did not stop")
	}
}
