// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"github.com/cB-Sigma/lwm2m"
	"github.com/cB-Sigma/lwm2m/client"
)

// Objects holds a simulated client's object instances (client.Objects).
type Objects = client.Objects

// NewObjects returns an empty object set typed by model.
func NewObjects(model lwm2m.Model) *Objects { return client.NewObjects(model) }

// Default is the demo object set with made-up values (client.Default).
func Default() *Objects { return client.Default() }
