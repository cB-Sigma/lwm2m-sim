// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package interop runs the simulated client against Eclipse Leshan's
// demo server and demo bootstrap server (2.0.0-M18) in Docker, through
// the demo server's REST API: registration, reads in every content format
// Leshan offers (Leshan decodes the simulator's payloads), writes,
// Write-Attributes, Execute, Discover, Create and Delete, Read-Composite,
// Observe with notifications in every format, Send, DTLS with a
// pre-shared key and the client-initiated bootstrap.
package interop

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/lwm2m"
	"github.com/cB-Sigma/lwm2m-sim/sim"
)

// leshanFormats are Leshan's names of the formats the simulator encodes,
// with whether they carry several values.
var leshanFormats = []struct {
	name  string
	multi bool
}{
	{"TLV", true}, {"SENML_JSON", true}, {"SENML_CBOR", true}, {"JSON", true}, {"TEXT", false}, {"CBOR", false},
}

// flatten turns a Leshan node (as the demo server writes it) into path →
// value, the way the simulator stores values: numbers as float64, times
// in seconds, opaque values as hex.
func flatten(base string, node map[string]any, out map[string]any) {
	id := fmt.Sprint(node["id"])
	if f, ok := node["id"].(float64); ok {
		id = strconv.Itoa(int(f))
	}
	path := base + "/" + id
	switch node["kind"] {
	case "obj":
		for _, x := range node["instances"].([]any) {
			flatten(path, x.(map[string]any), out)
		}
	case "instance":
		for _, x := range node["resources"].([]any) {
			flatten(path, x.(map[string]any), out)
		}
	case "multiResource":
		for ri, v := range node["values"].(map[string]any) {
			out[path+"/"+ri] = leshanValue(node["type"], v)
		}
	default:
		out[path] = leshanValue(node["type"], node["value"])
	}
}

func leshanValue(typ, v any) any {
	switch typ {
	case "INTEGER", "UNSIGNED_INTEGER", "FLOAT":
		f, _ := strconv.ParseFloat(fmt.Sprint(v), 64)
		return f
	case "TIME":
		if f, ok := v.(float64); ok {
			return math.Floor(f / 1000)
		}
	}
	return v
}

func startSim(t *testing.T, cfg sim.Config) *sim.Client {
	t.Helper()
	c := sim.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

type response struct {
	Status      string           `json:"status"`
	Success     bool             `json:"success"`
	Content     map[string]any   `json:"content"`
	ObjectLinks []map[string]any `json:"objectLinks"`
	Location    string           `json:"location"`
	Error       string           `json:"errormessage"`
}

func TestLeshanServerRegistersReadsAndWrites(t *testing.T) {
	ls := startLeshan(t, "server", "server.jar")
	const ep = "lwm2m-sim-1"
	c := startSim(t, sim.Config{Endpoint: ep, Server: "coap://" + ls.coap, Lifetime: 120 * time.Second})
	reg := ls.waitRegistered(t, ep)
	if reg["lwM2mVersion"] != "1.1" || fmt.Sprint(reg["lifetime"]) != "120" {
		t.Errorf("registration = %v", reg)
	}
	if links := fmt.Sprint(reg["objectLinks"]); !strings.Contains(links, "/3303/0") || !strings.Contains(links, "/3/0") {
		t.Errorf("object links = %s", links)
	}

	// Reads: Leshan decodes each format; the values must be the
	// simulator's.
	want := map[string]any{}
	for _, v := range []string{"/3/0", "/3303/0", "/4/0", "/1/0"} {
		p, _ := lwm2m.ParsePath(v)
		for _, r := range c.Objects().Values(p) {
			want[r.Path.String()] = r.V
		}
	}
	for _, f := range leshanFormats {
		for _, path := range []string{"/3/0", "/3303/0", "/4/0", "/1/0", "/3303/0/5700", "/3/0/0", "/3/0/13", "/1/0/6"} {
			if strings.Count(path, "/") < 3 && !f.multi {
				continue
			}
			var r response
			code := ls.do(t, http.MethodGet, "/api/clients/"+ep+path+"?format="+f.name, nil, &r)
			if code != 200 || !r.Success {
				t.Errorf("read %s as %s = %d %+v", path, f.name, code, r)
				continue
			}
			got := map[string]any{}
			parent := path[:strings.LastIndex(path, "/")]
			flatten(parent, r.Content, got)
			for k, v := range got {
				if fmt.Sprint(v) != fmt.Sprint(want[k]) {
					t.Errorf("%s read as %s: %s = %v (%T), simulator has %v", path, f.name, k, v, v, want[k])
				}
			}
			if len(got) == 0 {
				t.Errorf("read %s as %s: nothing decoded from %v", path, f.name, r.Content)
			}
		}
	}

	// Writes in every format: string, integer, boolean, float.
	for i, f := range leshanFormats {
		for _, w := range []struct {
			path, typ string
			value     any
			want      any
		}{
			{"/3/0/14", "string", fmt.Sprintf("+0%d:00", i), fmt.Sprintf("+0%d:00", i)},
			{"/1/0/3", "integer", strconv.Itoa(100 + i), float64(100 + i)},
			{"/1/0/6", "boolean", i%2 == 1, i%2 == 1},
			{"/3303/0/5603", "float", strconv.FormatFloat(-10.25-float64(i), 'f', -1, 64), -10.25 - float64(i)},
		} {
			id, _ := strconv.Atoi(w.path[strings.LastIndex(w.path, "/")+1:])
			var r response
			code := ls.do(t, http.MethodPut, "/api/clients/"+ep+w.path+"?format="+f.name,
				map[string]any{"id": id, "kind": "singleResource", "type": w.typ, "value": w.value}, &r)
			if code != 200 || !r.Success {
				t.Errorf("write %s as %s = %d %+v", w.path, f.name, code, r)
				continue
			}
			if v, _ := c.Objects().Get(w.path); fmt.Sprint(v) != fmt.Sprint(w.want) {
				t.Errorf("after writing %s as %s the simulator has %v, want %v", w.path, f.name, v, w.want)
			}
		}
	}
	// A partial update of an instance (POST, replace=false) in TLV.
	var r response
	if code := ls.do(t, http.MethodPut, "/api/clients/"+ep+"/3303/0?format=TLV&replace=false",
		map[string]any{"id": 0, "kind": "instance", "resources": []any{
			map[string]any{"id": 5701, "kind": "singleResource", "type": "string", "value": "K"},
			map[string]any{"id": 5750, "kind": "singleResource", "type": "string", "value": "boiler"},
		}}, &r); code != 200 || !r.Success {
		t.Errorf("partial update = %d %+v", code, r)
	}
	if v, _ := c.Objects().Get("/3303/0/5750"); v != "boiler" {
		t.Errorf("/3303/0/5750 = %v after the partial update", v)
	}

	// Write-Attributes, Execute, Discover.
	if code := ls.do(t, http.MethodPut, "/api/clients/"+ep+"/3303/0/5700/attributes?pmin=1&pmax=30", nil, &r); code != 200 || !r.Success {
		t.Errorf("write attributes = %d %+v", code, r)
	}
	if code := ls.do(t, http.MethodPost, "/api/clients/"+ep+"/3/0/4", nil, &r); code != 200 || !r.Success {
		t.Errorf("execute = %d %+v", code, r)
	}
	if code := ls.do(t, http.MethodPost, "/api/clients/"+ep+"/3/0/0", nil, &r); code != 200 || !strings.HasPrefix(r.Status, "METHOD_NOT_ALLOWED") {
		t.Errorf("execute of a readable resource = %d %+v, want METHOD_NOT_ALLOWED", code, r)
	}
	rec := strings.Join(c.Record(), "; ")
	if !strings.Contains(rec, "attributes /3303/0/5700 pmin=1&pmax=30") || !strings.Contains(rec, "execute /3/0/4") {
		t.Errorf("record = %s", rec)
	}
	for _, path := range []string{"/3", "/3303/0", "/3303/0/5700"} {
		var d response
		if code := ls.do(t, http.MethodGet, "/api/clients/"+ep+path+"/discover", nil, &d); code != 200 || !d.Success {
			t.Errorf("discover %s = %d %+v", path, code, d)
			continue
		}
		links := fmt.Sprint(d.ObjectLinks)
		t.Logf("discover %s = %s", path, links)
		if !strings.Contains(links, path) {
			t.Errorf("discover %s = %s", path, links)
		}
		if path == "/3303/0/5700" && !strings.Contains(links, "pmin") {
			t.Errorf("discover %s does not show the written attributes: %s", path, links)
		}
	}
	// The Security object is not readable by the server.
	if code := ls.do(t, http.MethodGet, "/api/clients/"+ep+"/0/0?format=TLV", nil, &r); code != 200 || !strings.HasPrefix(r.Status, "UNAUTHORIZED") {
		t.Errorf("read of /0/0 = %d %+v", code, r)
	}

	// Create and Delete an instance of 3303.
	if code := ls.do(t, http.MethodPost, "/api/clients/"+ep+"/3303?format=TLV",
		map[string]any{"id": 1, "kind": "instance", "resources": []any{
			map[string]any{"id": 5700, "kind": "singleResource", "type": "float", "value": "30.5"},
			map[string]any{"id": 5701, "kind": "singleResource", "type": "string", "value": "Cel"},
		}}, &r); code != 200 || !strings.HasPrefix(r.Status, "CREATED") {
		t.Errorf("create = %d %+v", code, r)
	}
	if v, _ := c.Objects().Get("/3303/1/5700"); v != 30.5 {
		t.Errorf("/3303/1/5700 = %v after create", v)
	}
	if code := ls.do(t, http.MethodDelete, "/api/clients/"+ep+"/3303/1", nil, &r); code != 200 || !strings.HasPrefix(r.Status, "DELETED") {
		t.Errorf("delete = %d %+v", code, r)
	}
	if _, ok := c.Objects().Get("/3303/1/5700"); ok {
		t.Error("/3303/1 is still there after delete")
	}

	// Read-Composite in SenML JSON and CBOR.
	for _, f := range []string{"SENML_JSON", "SENML_CBOR"} {
		var cr struct {
			Status  string                    `json:"status"`
			Success bool                      `json:"success"`
			Content map[string]map[string]any `json:"content"`
		}
		code := ls.do(t, http.MethodGet, "/api/clients/"+ep+"/composite?paths=/3/0/0,/3303/0/5700&pathformat="+f+"&nodeformat="+f, nil, &cr)
		if code != 200 || !cr.Success || len(cr.Content) != 2 {
			t.Errorf("read-composite as %s = %d %+v", f, code, cr)
			continue
		}
		if v := cr.Content["/3/0/0"]["value"]; v != want["/3/0/0"] {
			t.Errorf("read-composite /3/0/0 = %v", v)
		}
	}
}

func TestLeshanServerObservesInEveryFormat(t *testing.T) {
	ls := startLeshan(t, "server", "server.jar")
	const ep = "lwm2m-sim-observe"
	c := startSim(t, sim.Config{Endpoint: ep, Server: "coap://" + ls.coap})
	ls.waitRegistered(t, ep)
	events := ls.events(t, ep)
	temp := 22.0
	for _, f := range leshanFormats {
		for _, path := range []string{"/3303/0/5700", "/3303/0"} {
			if path == "/3303/0" && !f.multi {
				continue
			}
			t.Run(f.name+path, func(t *testing.T) {
				var r response
				if code := ls.do(t, http.MethodPost, "/api/clients/"+ep+path+"/observe?format="+f.name, nil, &r); code != 200 || !r.Success {
					t.Fatalf("observe = %d %+v", code, r)
				}
				defer ls.do(t, http.MethodDelete, "/api/clients/"+ep+path+"/observe?active", nil, nil)
				for i := 0; i < 3; i++ {
					temp += 0.5
					if err := c.Set("/3303/0/5700", temp); err != nil {
						t.Fatal(err)
					}
					d := next(t, events, "NOTIFICATION", 10*time.Second)
					got := map[string]any{}
					val, _ := d["val"].(map[string]any)
					parent := path[:strings.LastIndex(path, "/")]
					flatten(parent, val, got)
					if got["/3303/0/5700"] != temp {
						t.Errorf("notification %d = %v, want %v", i, got, temp)
					}
				}
			})
		}
	}
	if n := c.Observations(); n != 0 {
		t.Logf("%d observations left after active cancels", n)
	}

	// Send (LwM2M 1.1 information reporting).
	if err := c.Send(context.Background(), "/3303/0/5700", "/3/0/9"); err != nil {
		t.Fatalf("send: %v", err)
	}
	d := next(t, events, "SEND", 10*time.Second)
	if s := fmt.Sprint(d["val"]); !strings.Contains(s, "5700") {
		t.Errorf("send event = %v", d)
	}
}

func TestLeshanServerDTLSPSK(t *testing.T) {
	ls := startLeshan(t, "server", "server.jar")
	const ep = "lwm2m-sim-psk"
	key := "000102030405060708090a0b0c0d0e0f"
	if code := ls.do(t, http.MethodPut, "/api/security/clients",
		map[string]any{"endpoint": ep, "tls": map[string]any{"mode": "psk", "details": map[string]any{"identity": "sim-psk-id", "key": key}}}, nil); code >= 300 {
		t.Fatalf("security = %d", code)
	}
	c := startSim(t, sim.Config{Endpoint: ep, Server: "coaps://" + ls.coaps, PSKIdentity: "sim-psk-id",
		PSK: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}})
	ls.waitRegistered(t, ep)
	var r response
	if code := ls.do(t, http.MethodGet, "/api/clients/"+ep+"/3/0/0?format=SENML_CBOR", nil, &r); code != 200 || !r.Success || r.Content["value"] != "cB-Sigma demo" {
		t.Errorf("read over DTLS = %d %+v", code, r)
	}
	_ = c
	// A wrong key does not register.
	bad := sim.New(sim.Config{Endpoint: ep + "-bad", Server: "coaps://" + ls.coaps, PSKIdentity: "sim-psk-id",
		PSK: []byte("wrong-key-000000")})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := bad.Start(ctx); err == nil {
		_ = bad.Stop()
		t.Error("a client with the wrong key registered")
	}
}

func TestLeshanBootstrapServer(t *testing.T) {
	ls := startLeshan(t, "server", "server.jar")
	bs := startLeshan(t, "bs", "bsserver.jar")
	const ep = "lwm2m-sim-bs"
	// The bootstrap configuration: delete /0 and /1, write the LwM2M
	// server account (no security) and its Server object instance. The
	// server URI is as seen from the simulator, outside the containers.
	cfg := map[string]any{
		"toDelete": []string{"/0", "/1"},
		"servers": map[string]any{"0": map[string]any{"shortId": 123, "lifetime": 90, "defaultMinPeriod": 1,
			"binding": []string{"U"}, "notifIfDisabled": true}},
		"security": map[string]any{
			"0": map[string]any{"uri": "coap://" + bs.coap, "bootstrapServer": true, "securityMode": "NO_SEC",
				"publicKeyOrId": []any{}, "serverPublicKey": []any{}, "secretKey": []any{}, "serverId": 0},
			"1": map[string]any{"uri": "coap://" + ls.coap, "bootstrapServer": false, "securityMode": "NO_SEC",
				"publicKeyOrId": []any{}, "serverPublicKey": []any{}, "secretKey": []any{}, "serverId": 123},
		},
	}
	if code := bs.do(t, http.MethodPost, "/api/bootstrap/"+ep, cfg, nil); code >= 300 {
		t.Fatalf("bootstrap config = %d", code)
	}
	c := startSim(t, sim.Config{Endpoint: ep, Bootstrap: "coap://" + bs.coap})
	reg := ls.waitRegistered(t, ep)
	if fmt.Sprint(reg["lifetime"]) != "90" {
		t.Errorf("registration after bootstrap = %v", reg)
	}
	rec := strings.Join(c.Record(), "; ")
	if !strings.Contains(rec, "bootstrap-finish") || !strings.Contains(rec, "bootstrap-write") {
		t.Errorf("record = %s", rec)
	}
	t.Logf("record = %s", rec)
}
