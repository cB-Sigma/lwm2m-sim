// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package sim is a simulated LwM2M client device over CoAP/UDP or
// CoAP/DTLS. Since v0.2.0 the device logic is the lwm2m module's client
// package (the LwM2M client role as a library); this package keeps its
// names so code written against v0.1.0 builds unchanged, and adds the
// demo object set (Default) and the command in cmd/lwm2msim.
package sim

import "github.com/cB-Sigma/lwm2m/client"

// Config is a simulated client's configuration (client.Config).
type Config = client.Config

// Client is a simulated LwM2M client (client.Client).
type Client = client.Client

// New returns a client; Start connects it.
func New(cfg Config) *Client { return client.New(cfg) }
