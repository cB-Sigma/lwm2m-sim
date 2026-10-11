// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapnet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"

	"github.com/cB-Sigma/lwm2m"
	"github.com/cB-Sigma/lwm2m-sim/sim"
)

// server is a minimal LwM2M server (or bootstrap server) for the tests:
// it answers Register, Update, De-register, Bootstrap-Request and Send,
// and hands the client's connection to the test.
type server struct {
	addr  string
	mu    sync.Mutex
	regs  []string // query of each Register
	conns chan mux.Conn
	bs    chan mux.Conn
	sends chan []byte
	gone  chan struct{}
}

func newServer(t *testing.T) *server {
	t.Helper()
	l, err := coapnet.NewListenUDP("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{addr: l.LocalAddr().String(), conns: make(chan mux.Conn, 4), bs: make(chan mux.Conn, 4),
		sends: make(chan []byte, 4), gone: make(chan struct{}, 4)}
	r := mux.NewRouter()
	r.DefaultHandle(mux.HandlerFunc(s.serve))
	srv := udp.NewServer(options.WithMux(r))
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Stop(); _ = l.Close() })
	return s
}

// newDTLSServer is newServer over DTLS with one PSK identity.
func newDTLSServer(t *testing.T, identity string, key []byte) *server {
	t.Helper()
	l, err := coapnet.NewDTLSListener("udp4", "127.0.0.1:0", &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			if string(hint) != identity {
				return nil, fmt.Errorf("unknown identity %q", hint)
			}
			return key, nil
		},
		CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{addr: l.Addr().String(), conns: make(chan mux.Conn, 4), bs: make(chan mux.Conn, 4),
		sends: make(chan []byte, 4), gone: make(chan struct{}, 4)}
	r := mux.NewRouter()
	r.DefaultHandle(mux.HandlerFunc(s.serve))
	srv := coapdtls.NewServer(options.WithMux(r))
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Stop(); _ = l.Close() })
	return s
}

func (s *server) serve(w mux.ResponseWriter, r *mux.Message) {
	p, _ := r.Options().Path()
	p = "/" + strings.Trim(p, "/")
	qs, _ := r.Options().Queries()
	switch {
	case p == "/rd" && r.Code() == codes.POST:
		s.mu.Lock()
		s.regs = append(s.regs, strings.Join(qs, "&"))
		s.mu.Unlock()
		_ = w.SetResponse(codes.Created, message.TextPlain, nil,
			message.Option{ID: message.LocationPath, Value: []byte("rd")}, message.Option{ID: message.LocationPath, Value: []byte("5")})
		s.conns <- w.Conn()
	case p == "/rd/5" && r.Code() == codes.POST:
		_ = w.SetResponse(codes.Changed, message.TextPlain, nil)
	case p == "/rd/5" && r.Code() == codes.DELETE:
		_ = w.SetResponse(codes.Deleted, message.TextPlain, nil)
		s.gone <- struct{}{}
	case p == "/bs" && r.Code() == codes.POST:
		_ = w.SetResponse(codes.Changed, message.TextPlain, nil)
		s.bs <- w.Conn()
	case p == "/dp" && r.Code() == codes.POST:
		b, _ := io.ReadAll(r.Body())
		_ = w.SetResponse(codes.Changed, message.TextPlain, nil)
		s.sends <- b
	default:
		_ = w.SetResponse(codes.NotFound, message.TextPlain, nil)
	}
}

func (s *server) client(t *testing.T) mux.Conn {
	t.Helper()
	select {
	case c := <-s.conns:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no registration")
	}
	return nil
}

func accept(cf int) message.Option {
	var b []byte
	for v := uint32(cf); v > 0; v >>= 8 {
		b = append([]byte{byte(v)}, b...)
	}
	return message.Option{ID: message.Accept, Value: b}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func body(m *pool.Message) []byte {
	if m.Body() == nil {
		return nil
	}
	b, _ := io.ReadAll(m.Body())
	return b
}

func start(t *testing.T, cfg sim.Config) *sim.Client {
	t.Helper()
	c := sim.New(cfg)
	if err := c.Start(ctx(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

func values(vals []lwm2m.Value) map[string]string {
	out := map[string]string{}
	for _, v := range vals {
		if m, ok := v.V.(map[string]any); ok {
			for ri, x := range m {
				out[v.Path.String()+"/"+ri] = fmt.Sprint(x)
			}
			continue
		}
		out[v.Path.String()] = fmt.Sprint(v.V)
	}
	return out
}

func TestRegisterReadEveryFormat(t *testing.T) {
	s := newServer(t)
	c := start(t, sim.Config{Endpoint: "sim-1", Server: "coap://" + s.addr, Lifetime: time.Minute})
	cc := s.client(t)
	s.mu.Lock()
	reg := s.regs[0]
	s.mu.Unlock()
	for _, q := range []string{"ep=sim-1", "lt=60", "lwm2m=1.1", "b=U"} {
		if !strings.Contains(reg, q) {
			t.Errorf("register query %q lacks %s", reg, q)
		}
	}
	base, _ := lwm2m.ParsePath("/3/0")
	want := values(c.Objects().Values(base))
	for _, cf := range []int{lwm2m.CFTLV, lwm2m.CFSenMLJSON, lwm2m.CFSenMLCBOR, lwm2m.CFLwM2MJSON, lwm2m.CFLwM2MCBOR} {
		resp, err := cc.Get(ctx(t), "/3/0", accept(cf))
		if err != nil || resp.Code() != codes.Content {
			t.Fatalf("read as %d: %v %v", cf, resp, err)
		}
		got, err := lwm2m.Decode(cf, nil, base, body(resp))
		if err != nil || fmt.Sprint(values(got)) != fmt.Sprint(want) {
			t.Errorf("read as %d = %v %v, want %v", cf, values(got), err, want)
		}
	}
	for _, cf := range []int{lwm2m.CFText, lwm2m.CFCBOR} {
		resp, err := cc.Get(ctx(t), "/3303/0/5700", accept(cf))
		if err != nil || resp.Code() != codes.Content {
			t.Fatalf("read as %d: %v %v", cf, resp, err)
		}
		p, _ := lwm2m.ParsePath("/3303/0/5700")
		got, err := lwm2m.Decode(cf, nil, p, body(resp))
		if err != nil || got[0].V != 21.5 {
			t.Errorf("single read as %d = %v %v", cf, got, err)
		}
	}
	// Plain text cannot carry an instance; the Security object is closed;
	// executable resources cannot be read; unknown paths are 4.04.
	for _, c := range []struct {
		path string
		cf   int
		want codes.Code
	}{
		{"/3/0", lwm2m.CFText, codes.NotAcceptable},
		{"/0", lwm2m.CFTLV, codes.Unauthorized},
		{"/3/0/4", lwm2m.CFTLV, codes.MethodNotAllowed},
		{"/9999/0", lwm2m.CFTLV, codes.NotFound},
		{"/3/0", 1234, codes.NotAcceptable},
	} {
		resp, err := cc.Get(ctx(t), c.path, accept(c.cf))
		if err != nil || resp.Code() != c.want {
			t.Errorf("read %s as %d = %v %v, want %v", c.path, c.cf, resp.Code(), err, c.want)
		}
	}
	// Discover lists instances, resources, dimensions.
	resp, err := cc.Get(ctx(t), "/3", accept(40))
	if err != nil || resp.Code() != codes.Content {
		t.Fatalf("discover: %v %v", resp, err)
	}
	if d := string(body(resp)); !strings.Contains(d, "</3/0/6>;dim=2") || !strings.Contains(d, "</3/0/4>") {
		t.Errorf("discover = %s", d)
	}
	// Update and De-register.
	if err := c.Update(ctx(t)); err != nil {
		t.Errorf("update: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
	select {
	case <-s.gone:
	case <-time.After(5 * time.Second):
		t.Error("no de-registration")
	}
}

func TestWriteExecuteCreateDelete(t *testing.T) {
	s := newServer(t)
	c := start(t, sim.Config{Endpoint: "sim-2", Server: "coap://" + s.addr})
	cc := s.client(t)
	p, _ := lwm2m.ParsePath("/3/0/14")
	for i, cf := range []int{lwm2m.CFTLV, lwm2m.CFSenMLJSON, lwm2m.CFSenMLCBOR, lwm2m.CFLwM2MJSON, lwm2m.CFLwM2MCBOR, lwm2m.CFText, lwm2m.CFCBOR} {
		v := fmt.Sprintf("+0%d:00", i)
		b, err := lwm2m.Encode(cf, nil, p, []lwm2m.Value{{Path: p, V: v}})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := cc.Put(ctx(t), "/3/0/14", message.MediaType(cf), bytes.NewReader(b))
		if err != nil || resp.Code() != codes.Changed {
			t.Errorf("write as %d = %v %v", cf, resp, err)
		}
		if got, _ := c.Objects().Get("/3/0/14"); got != v {
			t.Errorf("after write as %d: %v", cf, got)
		}
	}
	// A multiple resource replaced in TLV.
	mp, _ := lwm2m.ParsePath("/3/0/6")
	b, _ := lwm2m.EncodeTLV(nil, mp, []lwm2m.Value{{Path: lwm2m.Path{Obj: 3, Inst: 0, Res: 6, RI: 3}, V: 7.0}})
	if resp, err := cc.Put(ctx(t), "/3/0/6", message.MediaType(lwm2m.CFTLV), bytes.NewReader(b)); err != nil || resp.Code() != codes.Changed {
		t.Errorf("multiple write = %v %v", resp, err)
	}
	if _, ok := c.Objects().Get("/3/0/6/0"); ok {
		t.Error("replace kept the old resource instances")
	}
	if v, _ := c.Objects().Get("/3/0/6/3"); v != 7.0 {
		t.Errorf("/3/0/6/3 = %v", v)
	}
	// Refusals.
	for _, w := range []struct {
		path string
		want codes.Code
	}{{"/3/0/4", codes.MethodNotAllowed}, {"/0/0/0", codes.Unauthorized}, {"/77/0/1", codes.NotFound}} {
		resp, err := cc.Put(ctx(t), w.path, message.TextPlain, strings.NewReader("1"))
		if err != nil || resp.Code() != w.want {
			t.Errorf("write %s = %v %v, want %v", w.path, resp.Code(), err, w.want)
		}
	}
	// Write-Attributes and Execute.
	resp, err := cc.Put(ctx(t), "/3303/0/5700", message.TextPlain, nil, message.Option{ID: message.URIQuery, Value: []byte("pmin=2")})
	if err != nil || resp.Code() != codes.Changed {
		t.Errorf("attributes = %v %v", resp, err)
	}
	done := make(chan string, 1)
	c.OnExecute("/3/0/4", func(args string) { done <- args })
	resp, err = cc.Post(ctx(t), "/3/0/4", message.TextPlain, strings.NewReader("0='now'"))
	if err != nil || resp.Code() != codes.Changed {
		t.Errorf("execute = %v %v", resp, err)
	}
	select {
	case a := <-done:
		if a != "0='now'" {
			t.Errorf("execute args = %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Error("execute handler not called")
	}
	if resp, err := cc.Post(ctx(t), "/3/0/0", message.TextPlain, nil); err != nil || resp.Code() != codes.MethodNotAllowed {
		t.Errorf("execute of a readable resource = %v %v", resp, err)
	}
	// Create an instance in SenML JSON, then delete it.
	b, _ = lwm2m.EncodeSenMLJSON([]lwm2m.Value{{Path: lwm2m.Path{Obj: 3303, Inst: 4, Res: 5700, RI: -1}, V: 30.0}})
	resp, err = cc.Post(ctx(t), "/3303", message.MediaType(lwm2m.CFSenMLJSON), bytes.NewReader(b))
	if err != nil || resp.Code() != codes.Created {
		t.Fatalf("create = %v %v", resp, err)
	}
	if loc, _ := resp.Options().LocationPath(); strings.Trim(loc, "/") != "3303/4" {
		t.Errorf("create location = %q", loc)
	}
	if v, _ := c.Objects().Get("/3303/4/5700"); v != 30.0 {
		t.Errorf("/3303/4/5700 = %v", v)
	}
	if resp, err := cc.Delete(ctx(t), "/3303/4"); err != nil || resp.Code() != codes.Deleted {
		t.Errorf("delete = %v %v", resp, err)
	}
	if resp, err := cc.Delete(ctx(t), "/3/0"); err != nil || resp.Code() != codes.MethodNotAllowed {
		t.Errorf("delete of the device = %v %v", resp, err)
	}
	rec := strings.Join(c.Record(), "; ")
	for _, want := range []string{"write /3/0/14=+00:00", "attributes /3303/0/5700 pmin=2", "execute /3/0/4 0='now'", "create /3303/4", "delete /3303/4"} {
		if !strings.Contains(rec, want) {
			t.Errorf("record lacks %q: %s", want, rec)
		}
	}
}

func TestObserveNotifyCancelAndSend(t *testing.T) {
	s := newServer(t)
	c := start(t, sim.Config{Endpoint: "sim-3", Server: "coap://" + s.addr})
	cc := s.client(t)
	for _, cf := range []int{lwm2m.CFTLV, lwm2m.CFSenMLCBOR, lwm2m.CFText} {
		var mu sync.Mutex
		var got []float64
		p, _ := lwm2m.ParsePath("/3303/0/5700")
		obs, err := cc.Observe(ctx(t), "/3303/0/5700", func(n *pool.Message) {
			v, err := lwm2m.Decode(cf, nil, p, body(n))
			if err == nil && len(v) == 1 {
				mu.Lock()
				got = append(got, v[0].V.(float64))
				mu.Unlock()
			}
		}, accept(cf))
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range []float64{22, 23.5} {
			if err := c.Set("/3303/0/5700", x); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(got)
			mu.Unlock()
			if n >= 3 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		if fmt.Sprint(got) != "[21.5 22 23.5]" && fmt.Sprint(got) != "[23.5 22 23.5]" {
			t.Errorf("format %d notifications = %v", cf, got)
		}
		mu.Unlock()
		if err := obs.Cancel(ctx(t)); err != nil {
			t.Errorf("cancel: %v", err)
		}
		_ = c.Set("/3303/0/5700", 21.5)
	}
	// Read-Composite.
	b, _ := lwm2m.EncodePaths(lwm2m.CFSenMLCBOR, []string{"/3/0/0", "/3303/0/5700"})
	req, _ := cc.NewGetRequest(ctx(t), "/")
	req.SetCode(codes.Code(5)) // FETCH
	req.SetContentFormat(message.MediaType(lwm2m.CFSenMLCBOR))
	req.SetAccept(message.MediaType(lwm2m.CFSenMLCBOR))
	req.SetBody(bytes.NewReader(b))
	resp, err := cc.Do(req)
	if err != nil || resp.Code() != codes.Content {
		t.Fatalf("read-composite = %v %v", resp, err)
	}
	tv, err := lwm2m.DecodeSenMLCBOR(body(resp))
	if err != nil || len(tv) != 2 || tv[0].V != "cB-Sigma demo" {
		t.Errorf("read-composite values = %v %v", tv, err)
	}
	// Send.
	if err := c.Send(ctx(t), "/3303/0/5700"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-s.sends:
		tv, err := lwm2m.DecodeSenMLCBOR(got)
		if err != nil || len(tv) != 1 || tv[0].Path.String() != "/3303/0/5700" {
			t.Errorf("send = %v %v", tv, err)
		}
	case <-time.After(5 * time.Second):
		t.Error("no send")
	}
}

func TestDTLSPSK(t *testing.T) {
	key := []byte("0123456789abcdef")
	s := newDTLSServer(t, "sim-psk", key)
	c := start(t, sim.Config{Endpoint: "sim-psk", Server: "coaps://" + s.addr, PSKIdentity: "sim-psk", PSK: key})
	cc := s.client(t)
	resp, err := cc.Get(ctx(t), "/3/0/0", accept(lwm2m.CFText))
	if err != nil || string(body(resp)) != "cB-Sigma demo" {
		t.Errorf("read over DTLS = %v %v", resp, err)
	}
	_ = c
	bad := sim.New(sim.Config{Endpoint: "x", Server: "coaps://" + s.addr, PSKIdentity: "sim-psk", PSK: []byte("wrong-key-000000")})
	if err := bad.Start(ctx(t)); err == nil {
		_ = bad.Stop()
		t.Error("a wrong key registered")
	}
}

func TestBootstrap(t *testing.T) {
	dm := newServer(t)
	bs := newServer(t)
	c := sim.New(sim.Config{Endpoint: "sim-bs", Bootstrap: "coap://" + bs.addr})
	errc := make(chan error, 1)
	go func() { errc <- c.Start(context.Background()) }()
	t.Cleanup(func() { _ = c.Stop() })
	var bc mux.Conn
	select {
	case bc = <-bs.bs:
	case <-time.After(5 * time.Second):
		t.Fatal("no bootstrap request")
	}
	// Bootstrap-Discover, Delete, Write and Finish, as a bootstrap server
	// sends them.
	resp, err := bc.Get(ctx(t), "/", accept(40))
	if err != nil || resp.Code() != codes.Content || !strings.Contains(string(body(resp)), "lwm2m=1.1") {
		t.Fatalf("bootstrap-discover = %v %v", resp, err)
	}
	if resp, err := bc.Delete(ctx(t), "/"); err != nil || resp.Code() != codes.Deleted {
		t.Fatalf("bootstrap-delete = %v %v", resp, err)
	}
	sec := lwm2m.EncodeInstanceTLV(1, map[int][]byte{0: []byte("coap://" + dm.addr), 1: {0}, 2: {3}, 10: {7}})
	if resp, err := bc.Put(ctx(t), "/0/1", message.MediaType(lwm2m.CFTLV), bytes.NewReader(sec)); err != nil || resp.Code() != codes.Changed {
		t.Fatalf("bootstrap-write /0/1 = %v %v", resp, err)
	}
	srv := lwm2m.EncodeInstanceTLV(0, map[int][]byte{0: {7}, 1: {45}, 7: []byte("U")})
	if resp, err := bc.Put(ctx(t), "/1/0", message.MediaType(lwm2m.CFTLV), bytes.NewReader(srv)); err != nil || resp.Code() != codes.Changed {
		t.Fatalf("bootstrap-write /1/0 = %v %v", resp, err)
	}
	if resp, err := bc.Post(ctx(t), "/bs", message.TextPlain, nil); err != nil || resp.Code() != codes.Changed {
		t.Fatalf("bootstrap-finish = %v %v", resp, err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	dm.client(t)
	dm.mu.Lock()
	reg := dm.regs[0]
	dm.mu.Unlock()
	if !strings.Contains(reg, "lt=45") {
		t.Errorf("register after bootstrap = %q", reg)
	}
}
