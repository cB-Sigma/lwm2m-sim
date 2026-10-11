// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"context"
	"fmt"
	"time"

	"github.com/cB-Sigma/lwm2m-sim/sim"
)

// A simulated device registers with an LwM2M server, changes its
// temperature (observers get a notification) and pushes it with Send.
// It needs a server, so it is not run.
func Example() {
	ctx := context.Background()
	c := sim.New(sim.Config{Endpoint: "demo-1", Server: "coap://127.0.0.1:5683", Lifetime: 5 * time.Minute})
	if err := c.Start(ctx); err != nil {
		fmt.Println(err)
		return
	}
	defer c.Stop()
	_ = c.Set("/3303/0/5700", 23.5)
	_ = c.Send(ctx, "/3303/0/5700")
	fmt.Println(c.Record()) // what the server did: reads are not recorded, writes and executes are
}

// Objects of your own: types come from the lwm2m package's model, or
// from a model you pass.
func ExampleObjects() {
	o := sim.NewObjects(nil)
	_ = o.Set("/3303/0/5700", 19.0)
	_ = o.Set("/3303/0/5701", "Cel")
	o.AddExecutable("/3303/0/5605")
	v, _ := o.Get("/3303/0/5700")
	fmt.Println(v)
	// Output: 19
}
