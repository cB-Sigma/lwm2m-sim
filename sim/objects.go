// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/cB-Sigma/lwm2m"
)

// Objects holds a simulated client's object instances and resource
// values, in the lwm2m package's conventions: numbers and times as
// float64, strings, booleans, opaque values as hex strings, object links
// as "obj:inst". A multiple-instance resource is one value per resource
// instance (a path with its resource instance). Executable resources have
// no value; they are listed so that Discover shows them.
type Objects struct {
	mu    sync.Mutex
	inst  map[[2]int]bool
	vals  map[lwm2m.Path]any
	exec  map[lwm2m.Path]bool
	model lwm2m.Model
}

// NewObjects returns an empty set of objects; types come from the
// lwm2m package's built-in model and from model (may be nil).
func NewObjects(model lwm2m.Model) *Objects {
	return &Objects{inst: map[[2]int]bool{}, vals: map[lwm2m.Path]any{}, exec: map[lwm2m.Path]bool{}, model: model}
}

// Default returns the objects of a simple device: Server (1) for short
// server ID 1, Device (3), Connectivity Monitoring (4) and an IPSO
// Temperature sensor (3303). Every value is a made-up demo value.
func Default() *Objects {
	o := NewObjects(nil)
	for p, v := range map[string]any{
		"/1/0/0": 1.0, "/1/0/1": 300.0, "/1/0/2": 1.0, "/1/0/3": 60.0, "/1/0/6": true, "/1/0/7": "U",
		"/3/0/0": "cB-Sigma demo", "/3/0/1": "lwm2m-sim", "/3/0/2": "SIM-0001", "/3/0/3": "1.0.0",
		"/3/0/6/0": 1.0, "/3/0/6/1": 5.0, "/3/0/7/0": 3800.0, "/3/0/7/1": 5000.0,
		"/3/0/9": 87.0, "/3/0/11/0": 0.0, "/3/0/13": 1700000000.0, "/3/0/14": "+05:30",
		"/3/0/15": "Asia/Kolkata", "/3/0/16": "U", "/3/0/17": "sensor", "/3/0/18": "1.0", "/3/0/19": "1.0.0",
		"/4/0/0": 6.0, "/4/0/1/0": 6.0, "/4/0/2": -71.0, "/4/0/3": 20.0, "/4/0/4/0": "192.0.2.10",
		"/4/0/8": 12345.0, "/4/0/9": 1.0, "/4/0/10": 404.0,
		"/3303/0/5700": 21.5, "/3303/0/5701": "Cel", "/3303/0/5601": 21.5, "/3303/0/5602": 21.5,
		"/3303/0/5603": -40.0, "/3303/0/5604": 85.0,
	} {
		if err := o.Set(p, v); err != nil {
			panic(err)
		}
	}
	for _, p := range []string{"/1/0/4", "/1/0/8", "/3/0/4", "/3/0/5", "/3/0/12", "/3303/0/5605"} {
		o.AddExecutable(p)
	}
	return o
}

// Model is the object model the client types values with.
func (o *Objects) Model() lwm2m.Model { return o.model }

// Type is the data type of the resource at p.
func (o *Objects) Type(p lwm2m.Path) string { return o.model.Lookup(p).Type }

// Set stores a resource (or resource instance) value, creating its
// object instance.
func (o *Objects) Set(path string, v any) error {
	p, err := lwm2m.ParsePath(path)
	if err != nil {
		return err
	}
	return o.SetPath(p, v)
}

// SetPath is Set with a parsed path.
func (o *Objects) SetPath(p lwm2m.Path, v any) error {
	if p.Inst < 0 || p.Res < 0 {
		return fmt.Errorf("%s is not a resource path", p)
	}
	if typ := o.Type(p); typ != "" {
		tv, err := lwm2m.TypedValue(typ, v)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if typ == lwm2m.TOpaque {
			tv = v // keep the hex string
		}
		v = tv
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inst[[2]int{p.Obj, p.Inst}] = true
	o.vals[p] = v
	return nil
}

// AddExecutable lists an executable resource.
func (o *Objects) AddExecutable(path string) {
	p, err := lwm2m.ParsePath(path)
	if err != nil || p.Res < 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inst[[2]int{p.Obj, p.Inst}] = true
	o.exec[p] = true
}

// Get returns the value at a resource or resource instance path.
func (o *Objects) Get(path string) (any, bool) {
	p, err := lwm2m.ParsePath(path)
	if err != nil {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.vals[p]
	return v, ok
}

// executable tells whether p is a listed executable resource.
func (o *Objects) executable(p lwm2m.Path) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.exec[lwm2m.Path{Obj: p.Obj, Inst: p.Inst, Res: p.Res, RI: -1}] || o.Type(p) == lwm2m.TExec
}

// exists tells whether something is at p (an object with an instance, an
// instance, a resource, a resource instance).
func (o *Objects) exists(p lwm2m.Path) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if p.Inst < 0 {
		for k := range o.inst {
			if k[0] == p.Obj {
				return true
			}
		}
		return false
	}
	if !o.inst[[2]int{p.Obj, p.Inst}] {
		return false
	}
	if p.Res < 0 {
		return true
	}
	for k := range o.vals {
		if under(k, p) {
			return true
		}
	}
	return o.exec[lwm2m.Path{Obj: p.Obj, Inst: p.Inst, Res: p.Res, RI: -1}]
}

// under tells whether k is p or below it.
func under(k, p lwm2m.Path) bool {
	return k.Obj == p.Obj && (p.Inst < 0 || k.Inst == p.Inst) && (p.Res < 0 || k.Res == p.Res) && (p.RI < 0 || k.RI == p.RI)
}

// Values returns the values at and below p, in path order.
func (o *Objects) Values(p lwm2m.Path) []lwm2m.Value { return o.values(p) }

// values returns the values at and below p, in path order.
func (o *Objects) values(p lwm2m.Path) []lwm2m.Value {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []lwm2m.Value
	for k, v := range o.vals {
		if under(k, p) {
			out = append(out, lwm2m.Value{Path: k, V: v})
		}
	}
	sortValues(out)
	return out
}

func sortValues(vs []lwm2m.Value) {
	sort.Slice(vs, func(i, j int) bool { return less(vs[i].Path, vs[j].Path) })
}

func less(a, b lwm2m.Path) bool {
	for _, d := range [][2]int{{a.Obj, b.Obj}, {a.Inst, b.Inst}, {a.Res, b.Res}, {a.RI, b.RI}} {
		if d[0] != d[1] {
			return d[0] < d[1]
		}
	}
	return false
}

// instances returns the object instances, in order.
func (o *Objects) instances() [][2]int {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([][2]int, 0, len(o.inst))
	for k := range o.inst {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// resources lists the resource paths of an instance or object (values
// and executables), in order.
func (o *Objects) resources(p lwm2m.Path) []lwm2m.Path {
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := map[lwm2m.Path]bool{}
	var out []lwm2m.Path
	add := func(k lwm2m.Path) {
		r := lwm2m.Path{Obj: k.Obj, Inst: k.Inst, Res: k.Res, RI: -1}
		if under(r, lwm2m.Path{Obj: p.Obj, Inst: p.Inst, Res: p.Res, RI: -1}) && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for k := range o.vals {
		add(k)
	}
	for k := range o.exec {
		add(k)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// deleteUnder removes everything at and below p (an object or an
// instance).
func (o *Objects) deleteUnder(p lwm2m.Path) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	found := false
	for k := range o.inst {
		if k[0] == p.Obj && (p.Inst < 0 || k[1] == p.Inst) {
			delete(o.inst, k)
			found = true
		}
	}
	for k := range o.vals {
		if under(k, p) {
			delete(o.vals, k)
		}
	}
	for k := range o.exec {
		if under(k, p) {
			delete(o.exec, k)
		}
	}
	return found
}

// clearResource removes the values of a resource (before a multiple
// resource is replaced).
func (o *Objects) clearResource(p lwm2m.Path) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k := range o.vals {
		if k.Obj == p.Obj && k.Inst == p.Inst && k.Res == p.Res {
			delete(o.vals, k)
		}
	}
}

// addInstance marks an instance as existing.
func (o *Objects) addInstance(obj, inst int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inst[[2]int{obj, inst}] = true
}

// freeInstance is the lowest instance ID not used in obj.
func (o *Objects) freeInstance(obj int) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := 0; ; i++ {
		if !o.inst[[2]int{obj, i}] {
			return i
		}
	}
}

// links is the object and instance list of a Register payload, objects
// without instances left out, and objects 0 (Security) left out as the
// registration interface asks.
func (o *Objects) links() []string {
	var out []string
	for _, k := range o.instances() {
		if k[0] == 0 {
			continue
		}
		out = append(out, "</"+strconv.Itoa(k[0])+"/"+strconv.Itoa(k[1])+">")
	}
	return out
}
