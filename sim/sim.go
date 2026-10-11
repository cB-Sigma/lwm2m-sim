// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package sim is a simulated LwM2M client device over CoAP/UDP or
// CoAP/DTLS with a pre-shared key. It registers with an LwM2M server (or
// bootstraps first), keeps its registration up to date, and answers the
// device management and information reporting interfaces: Read, Discover,
// Write (replace and partial update), Write-Attributes, Execute, Create,
// Delete, Read-Composite, Write-Composite, Observe and Cancel Observation,
// in every content format of the lwm2m package. It sends Notify when a
// value changes and Send on request. Clauses cited are of OMA LwM2M Core
// and Transport 1.1.1.
package sim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	piondtls "github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	udpclient "github.com/plgd-dev/go-coap/v3/udp/client"

	"github.com/cB-Sigma/lwm2m"
)

// Config is a simulated client's configuration.
type Config struct {
	// Endpoint is the client's endpoint name.
	Endpoint string
	// Server is the LwM2M server, "coap://host:port" or
	// "coaps://host:port" (port 5683 or 5684 when left out). It is not
	// used when Bootstrap is set: the bootstrap server provisions it.
	Server string
	// Bootstrap is a bootstrap server, "coap://..." or "coaps://...";
	// when set the client bootstraps first (client-initiated, Core
	// clause 5.2.7.1) and registers with the server it is given.
	Bootstrap string
	// PSKIdentity and PSK are the DTLS pre-shared key for coaps (for the
	// bootstrap server when Bootstrap is set).
	PSKIdentity string
	PSK         []byte
	// Lifetime of the registration (default 300 s); updates are sent at
	// half of it.
	Lifetime time.Duration
	// Version is the lwm2m= of Register: "1.0", "1.1" (default) or "1.2".
	Version string
	// Binding is the b= of Register (default "U").
	Binding string
	// Objects are the client's objects (Default when nil).
	Objects *Objects
	// Logger receives one line per request (slog.Default when nil).
	Logger *slog.Logger
}

// Client is a simulated LwM2M client. Its methods are safe for
// concurrent use.
type Client struct {
	cfg  Config
	objs *Objects
	log  *slog.Logger

	mu       sync.Mutex
	conn     *udpclient.Conn
	location string
	server   string
	psk      struct {
		id  string
		key []byte
	}
	observers []*observer
	attrs     map[string]string // path → query of the last Write-Attributes
	record    []string
	execs     map[string]func(args string)
	booted    chan struct{}
	stop      chan struct{}
	done      chan struct{}
	seq       uint32
}

type observer struct {
	paths []lwm2m.Path // one, or several for Observe-Composite
	token []byte
	cf    int
}

// New returns a client; Start connects it.
func New(cfg Config) *Client {
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = 300 * time.Second
	}
	if cfg.Version == "" {
		cfg.Version = "1.1"
	}
	if cfg.Binding == "" {
		cfg.Binding = "U"
	}
	if cfg.Objects == nil {
		cfg.Objects = Default()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	c := &Client{cfg: cfg, objs: cfg.Objects, log: cfg.Logger.With("endpoint", cfg.Endpoint),
		attrs: map[string]string{}, execs: map[string]func(string){}, stop: make(chan struct{}), done: make(chan struct{})}
	c.execs["/1/0/8"] = func(string) { go func() { _ = c.Update(context.Background()) }() }
	c.execs["/3303/0/5605"] = func(string) {
		if v, ok := c.objs.Get("/3303/0/5700"); ok {
			_ = c.Set("/3303/0/5601", v)
			_ = c.Set("/3303/0/5602", v)
		}
	}
	return c
}

// Objects returns the client's objects.
func (c *Client) Objects() *Objects { return c.objs }

// OnExecute sets what an Execute of path does (after it is recorded and
// answered 2.04).
func (c *Client) OnExecute(path string, f func(args string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execs[path] = f
}

// Record returns what the server did to the client, in order: "write
// /3/0/14=+01:00", "execute /3/0/4 args", "create /3303/1", "delete
// /3303/1", "attributes /3303/0/5700 pmin=1&pmax=5", "observe
// /3303/0/5700", "cancel /3303/0/5700", "bootstrap-write /0/1",
// "bootstrap-finish".
func (c *Client) Record() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.record...)
}

func (c *Client) note(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	c.mu.Lock()
	c.record = append(c.record, s)
	c.mu.Unlock()
	c.log.Debug("lwm2m-sim: " + s)
}

// Observations is the number of active observations.
func (c *Client) Observations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.observers)
}

// Location is the registration's location path ("" when not registered).
func (c *Client) Location() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.location
}

// Start bootstraps when configured, registers, and keeps the registration
// up to date until Stop.
func (c *Client) Start(ctx context.Context) error {
	server, id, key := c.cfg.Server, c.cfg.PSKIdentity, c.cfg.PSK
	if c.cfg.Bootstrap != "" {
		var err error
		if server, id, key, err = c.bootstrap(ctx); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.server, c.psk.id, c.psk.key = server, id, key
	c.mu.Unlock()
	if err := c.connect(server, id, key); err != nil {
		return err
	}
	if err := c.register(ctx); err != nil {
		return err
	}
	go c.keepAlive()
	return nil
}

// dial opens a CoAP connection to uri with the client's request handler.
func (c *Client) dial(uri, id string, key []byte) (*udpclient.Conn, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("lwm2m-sim: server %q is not a coap:// or coaps:// URI", uri)
	}
	host := u.Host
	r := mux.NewRouter()
	r.DefaultHandle(mux.HandlerFunc(c.serve))
	switch u.Scheme {
	case "coap":
		if u.Port() == "" {
			host += ":5683"
		}
		return udp.Dial(host, options.WithMux(r))
	case "coaps":
		if u.Port() == "" {
			host += ":5684"
		}
		if id == "" || len(key) == 0 {
			return nil, errors.New("lwm2m-sim: coaps needs a PSK identity and key")
		}
		cfg := &piondtls.Config{
			PSK:             func([]byte) ([]byte, error) { return key, nil },
			PSKIdentityHint: []byte(id),
			CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8, piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256},
		}
		// The handshake is done here, with a time limit, so that a wrong
		// key fails Start instead of blocking the first request.
		raddr, err := net.ResolveUDPAddr("udp", host)
		if err != nil {
			return nil, err
		}
		pc, err := net.DialUDP("udp", nil, raddr)
		if err != nil {
			return nil, err
		}
		dc, err := piondtls.Client(dtlsnet.PacketConnFromConn(pc), raddr, cfg) //nolint:staticcheck // as go-coap's own Dial
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		hctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := dc.HandshakeContext(hctx); err != nil {
			_ = dc.Close()
			return nil, fmt.Errorf("lwm2m-sim: DTLS handshake: %w", err)
		}
		return coapdtls.Client(dc, options.WithMux(r), options.WithCloseSocket()), nil
	}
	return nil, fmt.Errorf("lwm2m-sim: scheme %q is not coap or coaps", u.Scheme)
}

func (c *Client) connect(uri, id string, key []byte) error {
	cc, err := c.dial(uri, id, key)
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.conn
	c.conn = cc
	c.observers = nil
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// registerPayload is the CoRE link list of Register and Update.
func (c *Client) registerPayload() string {
	root := `</>;rt="oma.lwm2m"`
	if c.cfg.Version != "1.0" {
		root += `;ct=11542`
	}
	return strings.Join(append([]string{root}, c.objs.links()...), ",")
}

func (c *Client) register(ctx context.Context) error {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	q := func(s string) message.Option { return message.Option{ID: message.URIQuery, Value: []byte(s)} }
	opts := []message.Option{q("ep=" + c.cfg.Endpoint), q("lt=" + strconv.Itoa(int(c.cfg.Lifetime/time.Second))),
		q("lwm2m=" + c.cfg.Version), q("b=" + c.cfg.Binding)}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := cc.Post(rctx, "/rd", message.AppLinkFormat, strings.NewReader(c.registerPayload()), opts...)
	if err != nil {
		return fmt.Errorf("lwm2m-sim: register: %w", err)
	}
	if resp.Code() != codes.Created {
		return fmt.Errorf("lwm2m-sim: register: %v", resp.Code())
	}
	loc, err := resp.Options().LocationPath()
	if err != nil || loc == "" {
		return errors.New("lwm2m-sim: register: no Location-Path in the answer")
	}
	c.mu.Lock()
	c.location = "/" + strings.Trim(loc, "/")
	c.mu.Unlock()
	c.log.Info("lwm2m-sim: registered", "location", loc)
	return nil
}

// Update sends a registration update (with the object list); a 4.04
// answer makes the client register again.
func (c *Client) Update(ctx context.Context) error {
	c.mu.Lock()
	cc, loc := c.conn, c.location
	c.mu.Unlock()
	if cc == nil || loc == "" {
		return errors.New("lwm2m-sim: not registered")
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := cc.Post(rctx, loc, message.AppLinkFormat, strings.NewReader(c.registerPayload()))
	if err != nil {
		return fmt.Errorf("lwm2m-sim: update: %w", err)
	}
	switch resp.Code() {
	case codes.Changed:
		return nil
	case codes.NotFound:
		return c.register(ctx)
	}
	return fmt.Errorf("lwm2m-sim: update: %v", resp.Code())
}

func (c *Client) keepAlive() {
	defer close(c.done)
	t := time.NewTicker(c.cfg.Lifetime / 2)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			if err := c.Update(context.Background()); err != nil {
				c.log.Warn("lwm2m-sim: update failed", "err", err)
			}
		}
	}
}

// Stop de-registers and closes the connection.
func (c *Client) Stop() error {
	c.mu.Lock()
	cc, loc := c.conn, c.location
	c.location = ""
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	c.mu.Unlock()
	if cc == nil {
		return nil
	}
	var err error
	if loc != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = cc.Delete(ctx, loc)
		cancel()
	}
	_ = cc.Close()
	return err
}

// Set changes a resource value and notifies the observations it falls
// under.
func (c *Client) Set(path string, v any) error {
	p, err := lwm2m.ParsePath(path)
	if err != nil {
		return err
	}
	if err := c.objs.SetPath(p, v); err != nil {
		return err
	}
	c.changed(p)
	return nil
}

// changed notifies the observers of paths at or above p.
func (c *Client) changed(p lwm2m.Path) {
	c.mu.Lock()
	cc := c.conn
	var due []*observer
	for _, o := range c.observers {
		for _, op := range o.paths {
			if under(p, op) {
				due = append(due, o)
				break
			}
		}
	}
	c.mu.Unlock()
	for _, o := range due {
		b, err := c.encodeFor(o.paths, o.cf)
		if err != nil {
			c.log.Warn("lwm2m-sim: notify", "err", err)
			continue
		}
		c.mu.Lock()
		c.seq++
		seq := c.seq
		c.mu.Unlock()
		m := cc.AcquireMessage(cc.Context())
		m.SetCode(codes.Content)
		m.SetToken(o.token)
		m.SetContentFormat(message.MediaType(o.cf))
		m.SetObserve(seq)
		m.SetBody(bytes.NewReader(b))
		if err := cc.WriteMessage(m); err != nil {
			// A reset or no acknowledgement ends the observation.
			c.drop(o)
		}
		cc.ReleaseMessage(m)
	}
}

func (c *Client) drop(o *observer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.observers {
		if x == o {
			c.observers = append(c.observers[:i], c.observers[i+1:]...)
			return
		}
	}
}

// Send sends the values at paths to the server with the Send operation
// (Core clause 6.4.3, POST /dp) in SenML CBOR.
func (c *Client) Send(ctx context.Context, paths ...string) error {
	var vals []lwm2m.Value
	for _, s := range paths {
		p, err := lwm2m.ParsePath(s)
		if err != nil {
			return err
		}
		vals = append(vals, c.objs.values(p)...)
	}
	b, err := lwm2m.Encode(lwm2m.CFSenMLCBOR, c.objs.Model(), lwm2m.Path{Obj: -1, Inst: -1, Res: -1, RI: -1}, vals)
	if err != nil {
		return err
	}
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return errors.New("lwm2m-sim: not connected")
	}
	resp, err := cc.Post(ctx, "/dp", message.MediaType(lwm2m.CFSenMLCBOR), bytes.NewReader(b))
	if err != nil {
		return err
	}
	if resp.Code() != codes.Changed {
		return fmt.Errorf("lwm2m-sim: send: %v", resp.Code())
	}
	return nil
}

// ---- requests from the server --------------------------------------------

// codeFETCH and codeIPATCH are the CoAP methods of RFC 8132 that the
// composite operations use.
const (
	codeFETCH  = codes.Code(5)
	codeIPATCH = codes.Code(7)
)

// supported are the Content-Formats the client answers reads in.
var supported = map[int]bool{lwm2m.CFTLV: true, lwm2m.CFSenMLJSON: true, lwm2m.CFSenMLCBOR: true,
	lwm2m.CFLwM2MJSON: true, lwm2m.CFLwM2MCBOR: true, lwm2m.CFText: true, lwm2m.CFCBOR: true, lwm2m.CFOpaque: true}

func readBody(r *mux.Message) []byte {
	if r.Body() == nil {
		return nil
	}
	b, _ := io.ReadAll(r.Body())
	return b
}

func respond(w mux.ResponseWriter, code codes.Code) {
	_ = w.SetResponse(code, message.TextPlain, nil)
}

func (c *Client) serve(w mux.ResponseWriter, r *mux.Message) {
	raw, _ := r.Options().Path()
	raw = "/" + strings.Trim(raw, "/")
	c.log.Debug("lwm2m-sim: request", "code", r.Code(), "path", raw)
	if raw == "/bs" && r.Code() == codes.POST {
		c.bootstrapFinish(w)
		return
	}
	if raw == "/" {
		switch r.Code() {
		case codes.GET:
			c.discover(w, lwm2m.Path{Obj: -1, Inst: -1, Res: -1, RI: -1}, true)
		case codes.DELETE:
			c.bootstrapDelete(w, lwm2m.Path{Obj: -1, Inst: -1, Res: -1, RI: -1})
		case codeFETCH:
			c.readComposite(w, r)
		case codeIPATCH:
			c.writeComposite(w, r)
		default:
			respond(w, codes.MethodNotAllowed)
		}
		return
	}
	p, err := lwm2m.ParsePath(raw)
	if err != nil {
		respond(w, codes.NotFound)
		return
	}
	switch r.Code() {
	case codes.GET:
		if cf, err := r.Options().Accept(); err == nil && cf == message.AppLinkFormat {
			c.discover(w, p, c.bootstrapping())
			return
		}
		c.read(w, r, p)
	case codes.PUT:
		c.write(w, r, p, true)
	case codes.POST:
		switch {
		case p.Inst < 0:
			c.create(w, r, p)
		case p.Res < 0:
			c.write(w, r, p, false)
		default:
			c.execute(w, r, p)
		}
	case codes.DELETE:
		if c.bootstrapping() {
			c.bootstrapDelete(w, p)
			return
		}
		c.deleteInstance(w, p)
	case codeFETCH:
		c.readComposite(w, r)
	case codeIPATCH:
		c.writeComposite(w, r)
	default:
		respond(w, codes.MethodNotAllowed)
	}
}

// encodeFor encodes the values under paths in cf; several paths (a
// composite request) are written as one SenML or LwM2M CBOR pack.
func (c *Client) encodeFor(paths []lwm2m.Path, cf int) ([]byte, error) {
	var vals []lwm2m.Value
	for _, p := range paths {
		vals = append(vals, c.objs.values(p)...)
	}
	base := paths[0]
	if len(paths) > 1 {
		base = lwm2m.Path{Obj: -1, Inst: -1, Res: -1, RI: -1}
	}
	if (cf == lwm2m.CFText || cf == lwm2m.CFCBOR || cf == lwm2m.CFOpaque) && base.Res >= 0 && base.RI < 0 && len(vals) == 1 {
		return lwm2m.Encode(cf, c.objs.Model(), vals[0].Path, vals)
	}
	return lwm2m.Encode(cf, c.objs.Model(), base, vals)
}

// defaultFormat is the format of a read without Accept: plain text for
// a single resource, TLV otherwise (SenML CBOR in 1.2, where TLV is
// still allowed but SenML is the common choice).
func (c *Client) defaultFormat(p lwm2m.Path) int {
	if p.Res >= 0 && !c.multiple(p) {
		return lwm2m.CFText
	}
	if c.cfg.Version == "1.2" {
		return lwm2m.CFSenMLCBOR
	}
	return lwm2m.CFTLV
}

// multiple tells whether the resource at p has resource instances.
func (c *Client) multiple(p lwm2m.Path) bool {
	if p.RI >= 0 {
		return false
	}
	for _, v := range c.objs.values(p) {
		if v.Path.RI >= 0 {
			return true
		}
	}
	return false
}

func (c *Client) readable(w mux.ResponseWriter, p lwm2m.Path) bool {
	if p.Obj == 0 && !c.bootstrapping() {
		respond(w, codes.Unauthorized) // the Security object is not for the server (Core clause E.1)
		return false
	}
	if !c.objs.exists(p) {
		respond(w, codes.NotFound)
		return false
	}
	if p.Res >= 0 && c.objs.executable(p) {
		respond(w, codes.MethodNotAllowed)
		return false
	}
	return true
}

func (c *Client) read(w mux.ResponseWriter, r *mux.Message, p lwm2m.Path) {
	if !c.readable(w, p) {
		return
	}
	cf := c.defaultFormat(p)
	if a, err := r.Options().Accept(); err == nil {
		cf = int(a)
	}
	if !supported[cf] {
		respond(w, codes.NotAcceptable)
		return
	}
	b, err := c.encodeFor([]lwm2m.Path{p}, cf)
	if err != nil {
		if errors.Is(err, lwm2m.ErrNotSingle) {
			respond(w, codes.NotAcceptable)
			return
		}
		c.log.Warn("lwm2m-sim: read", "path", p, "err", err)
		respond(w, codes.InternalServerError)
		return
	}
	obs, obsErr := r.Options().Observe()
	if obsErr == nil && obs == 0 {
		o := &observer{paths: []lwm2m.Path{p}, token: append([]byte(nil), r.Token()...), cf: cf}
		c.mu.Lock()
		c.observers = append(c.observers, o)
		c.seq++
		seq := c.seq
		c.mu.Unlock()
		c.note("observe %s", p)
		_ = w.SetResponse(codes.Content, message.MediaType(cf), bytes.NewReader(b))
		w.Message().SetObserve(seq)
		return
	}
	if obsErr == nil && obs == 1 {
		c.cancel(r.Token(), p)
	}
	_ = w.SetResponse(codes.Content, message.MediaType(cf), bytes.NewReader(b))
}

// cancel ends the observation with token (Cancel Observation by a GET
// with Observe 1).
func (c *Client) cancel(token []byte, p lwm2m.Path) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, o := range c.observers {
		if bytes.Equal(o.token, token) {
			c.observers = append(c.observers[:i], c.observers[i+1:]...)
			c.record = append(c.record, "cancel "+p.String())
			return
		}
	}
}

// discover answers Discover (and Bootstrap-Discover when bs) in CoRE
// link format: the path, its instances and resources, and the
// attributes written at each.
func (c *Client) discover(w mux.ResponseWriter, p lwm2m.Path, bs bool) {
	var links []string
	attr := func(s string) string {
		c.mu.Lock()
		defer c.mu.Unlock()
		if q := c.attrs[s]; q != "" {
			return ";" + strings.ReplaceAll(q, "&", ";")
		}
		return ""
	}
	switch {
	case bs:
		links = append(links, `</>;lwm2m=`+c.cfg.Version)
		for _, k := range c.objs.instances() {
			if p.Obj >= 0 && k[0] != p.Obj {
				continue
			}
			l := "</" + strconv.Itoa(k[0]) + "/" + strconv.Itoa(k[1]) + ">"
			if k[0] == 1 {
				if v, ok := c.objs.Get("/1/" + strconv.Itoa(k[1]) + "/0"); ok {
					l += ";ssid=" + strconv.Itoa(int(v.(float64)))
				}
			}
			links = append(links, l)
		}
	default:
		if !c.objs.exists(p) {
			respond(w, codes.NotFound)
			return
		}
		links = append(links, "<"+p.String()+">"+attr(p.String()))
		if p.Res < 0 {
			for _, k := range c.objs.instances() {
				if k[0] != p.Obj || (p.Inst >= 0 && k[1] != p.Inst) {
					continue
				}
				ip := "/" + strconv.Itoa(k[0]) + "/" + strconv.Itoa(k[1])
				if p.Inst < 0 {
					links = append(links, "<"+ip+">"+attr(ip))
				}
				for _, rp := range c.objs.resources(lwm2m.Path{Obj: k[0], Inst: k[1], Res: -1, RI: -1}) {
					l := "<" + rp.String() + ">" + attr(rp.String())
					if n := c.instancesOf(rp); n > 0 {
						l += ";dim=" + strconv.Itoa(n)
					}
					links = append(links, l)
				}
			}
		}
	}
	_ = w.SetResponse(codes.Content, message.AppLinkFormat, strings.NewReader(strings.Join(links, ",")))
}

// instancesOf counts the resource instances of a multiple resource.
func (c *Client) instancesOf(p lwm2m.Path) int {
	n := 0
	for _, v := range c.objs.values(p) {
		if v.Path.RI >= 0 {
			n++
		}
	}
	return n
}

// decodeWrite decodes a Write, Create or Bootstrap-Write payload.
func (c *Client) decodeWrite(r *mux.Message, p lwm2m.Path) ([]lwm2m.Value, int, error) {
	cf := lwm2m.CFText
	if x, err := r.Options().ContentFormat(); err == nil {
		cf = int(x)
	}
	if !supported[cf] {
		return nil, cf, errUnsupported
	}
	vals, err := lwm2m.Decode(cf, c.objs.Model(), p, readBody(r))
	if err != nil {
		return nil, cf, err
	}
	// A multiple resource decoded from TLV is one value holding a map.
	var out []lwm2m.Value
	for _, v := range vals {
		if m, ok := v.V.(map[string]any); ok {
			for ri, x := range m {
				n, _ := strconv.Atoi(ri)
				q := v.Path
				q.RI = n
				out = append(out, lwm2m.Value{Path: q, V: x})
			}
			continue
		}
		out = append(out, v)
	}
	return out, cf, nil
}

var errUnsupported = errors.New("unsupported content format")

func (c *Client) write(w mux.ResponseWriter, r *mux.Message, p lwm2m.Path, replace bool) {
	body := readBody(r)
	r.SetBody(bytes.NewReader(body))
	if qs, _ := r.Options().Queries(); len(qs) > 0 && len(body) == 0 && replace {
		// Write-Attributes (Core clause 5.4.4).
		if !c.objs.exists(p) {
			respond(w, codes.NotFound)
			return
		}
		c.mu.Lock()
		c.attrs[p.String()] = strings.Join(qs, "&")
		c.mu.Unlock()
		c.note("attributes %s %s", p, strings.Join(qs, "&"))
		respond(w, codes.Changed)
		return
	}
	if c.bootstrapping() {
		c.bootstrapWrite(w, r, p)
		return
	}
	if p.Obj == 0 {
		respond(w, codes.Unauthorized)
		return
	}
	if !c.objs.exists(lwm2m.Path{Obj: p.Obj, Inst: p.Inst, Res: -1, RI: -1}) {
		respond(w, codes.NotFound)
		return
	}
	if p.Res >= 0 && c.objs.executable(p) {
		respond(w, codes.MethodNotAllowed)
		return
	}
	vals, cf, err := c.decodeWrite(r, p)
	if errors.Is(err, errUnsupported) {
		respond(w, codes.UnsupportedMediaType)
		return
	}
	if err != nil || len(vals) == 0 {
		c.log.Warn("lwm2m-sim: write", "path", p, "format", cf, "err", err)
		respond(w, codes.BadRequest)
		return
	}
	for _, v := range vals {
		if !under(v.Path, p) || v.Path.Res < 0 {
			respond(w, codes.BadRequest)
			return
		}
	}
	if replace && p.Res >= 0 && p.RI < 0 {
		c.objs.clearResource(p)
	}
	for _, v := range vals {
		if err := c.objs.SetPath(v.Path, v.V); err != nil {
			c.log.Warn("lwm2m-sim: write", "path", v.Path, "err", err)
			respond(w, codes.BadRequest)
			return
		}
		c.note("write %s=%v", v.Path, v.V)
	}
	respond(w, codes.Changed)
	go func() {
		for _, v := range vals {
			c.changed(v.Path)
		}
	}()
}

func (c *Client) execute(w mux.ResponseWriter, r *mux.Message, p lwm2m.Path) {
	if !c.objs.exists(p) {
		respond(w, codes.NotFound)
		return
	}
	if !c.objs.executable(p) {
		respond(w, codes.MethodNotAllowed)
		return
	}
	args := string(readBody(r))
	c.note("execute %s %s", p, args)
	c.mu.Lock()
	f := c.execs[p.String()]
	c.mu.Unlock()
	respond(w, codes.Changed)
	if f != nil {
		go f(args)
	}
}

func (c *Client) create(w mux.ResponseWriter, r *mux.Message, p lwm2m.Path) {
	if p.Obj == 0 || p.Obj == 1 || p.Obj == 3 {
		respond(w, codes.MethodNotAllowed)
		return
	}
	vals, _, err := c.decodeWrite(r, p)
	if errors.Is(err, errUnsupported) {
		respond(w, codes.UnsupportedMediaType)
		return
	}
	if err != nil || len(vals) == 0 {
		respond(w, codes.BadRequest)
		return
	}
	inst := -1
	for i := range vals {
		if vals[i].Path.Inst >= 0 {
			inst = vals[i].Path.Inst
		}
	}
	if inst < 0 {
		inst = c.objs.freeInstance(p.Obj)
	} else if c.objs.exists(lwm2m.Path{Obj: p.Obj, Inst: inst, Res: -1, RI: -1}) {
		respond(w, codes.BadRequest)
		return
	}
	c.objs.addInstance(p.Obj, inst)
	for _, v := range vals {
		v.Path.Obj, v.Path.Inst = p.Obj, inst
		if err := c.objs.SetPath(v.Path, v.V); err != nil {
			c.objs.deleteUnder(lwm2m.Path{Obj: p.Obj, Inst: inst, Res: -1, RI: -1})
			respond(w, codes.BadRequest)
			return
		}
	}
	c.note("create /%d/%d", p.Obj, inst)
	_ = w.SetResponse(codes.Created, message.TextPlain, nil,
		message.Option{ID: message.LocationPath, Value: []byte(strconv.Itoa(p.Obj))},
		message.Option{ID: message.LocationPath, Value: []byte(strconv.Itoa(inst))})
	go func() { _ = c.Update(context.Background()) }()
}

func (c *Client) deleteInstance(w mux.ResponseWriter, p lwm2m.Path) {
	if p.Inst < 0 || p.Res >= 0 || p.Obj == 0 || p.Obj == 1 || p.Obj == 3 {
		respond(w, codes.MethodNotAllowed)
		return
	}
	if !c.objs.deleteUnder(p) {
		respond(w, codes.NotFound)
		return
	}
	c.note("delete %s", p)
	respond(w, codes.Deleted)
	go func() { _ = c.Update(context.Background()) }()
}

// compositePaths reads the paths of a composite request (a SenML pack of
// names).
func compositePaths(r *mux.Message, body []byte) ([]lwm2m.Path, int, error) {
	cf, err := r.Options().ContentFormat()
	if err != nil {
		return nil, 0, errUnsupported
	}
	var tv []lwm2m.Path
	switch int(cf) {
	case lwm2m.CFSenMLJSON, lwm2m.CFSenMLEtchJSON:
		tv, err = decodeNames(body, false)
	case lwm2m.CFSenMLCBOR, lwm2m.CFSenMLEtchCBOR:
		tv, err = decodeNames(body, true)
	default:
		return nil, int(cf), errUnsupported
	}
	if err != nil {
		return nil, int(cf), err
	}
	out := make([]lwm2m.Path, 0, len(tv))
	for _, v := range tv {
		out = append(out, v)
	}
	return out, int(cf), nil
}

func (c *Client) readComposite(w mux.ResponseWriter, r *mux.Message) {
	paths, _, err := compositePaths(r, readBody(r))
	if errors.Is(err, errUnsupported) {
		respond(w, codes.UnsupportedMediaType)
		return
	}
	if err != nil || len(paths) == 0 {
		respond(w, codes.BadRequest)
		return
	}
	cf := lwm2m.CFSenMLCBOR
	if a, err := r.Options().Accept(); err == nil {
		cf = int(a)
	}
	if cf != lwm2m.CFSenMLCBOR && cf != lwm2m.CFSenMLJSON && cf != lwm2m.CFLwM2MCBOR {
		respond(w, codes.NotAcceptable)
		return
	}
	for _, p := range paths {
		if p.Obj == 0 || !c.objs.exists(p) {
			respond(w, codes.NotFound)
			return
		}
	}
	b, err := c.encodeFor(paths, cf)
	if err != nil {
		respond(w, codes.InternalServerError)
		return
	}
	if obs, err := r.Options().Observe(); err == nil && obs == 0 {
		o := &observer{paths: paths, token: append([]byte(nil), r.Token()...), cf: cf}
		c.mu.Lock()
		c.observers = append(c.observers, o)
		c.seq++
		seq := c.seq
		c.mu.Unlock()
		c.note("observe-composite %d paths", len(paths))
		_ = w.SetResponse(codes.Content, message.MediaType(cf), bytes.NewReader(b))
		w.Message().SetObserve(seq)
		return
	}
	_ = w.SetResponse(codes.Content, message.MediaType(cf), bytes.NewReader(b))
}

func (c *Client) writeComposite(w mux.ResponseWriter, r *mux.Message) {
	cf, err := r.Options().ContentFormat()
	if err != nil || (int(cf) != lwm2m.CFSenMLJSON && int(cf) != lwm2m.CFSenMLCBOR && int(cf) != lwm2m.CFSenMLEtchJSON && int(cf) != lwm2m.CFSenMLEtchCBOR) {
		respond(w, codes.UnsupportedMediaType)
		return
	}
	decode := lwm2m.CFSenMLJSON
	if int(cf) == lwm2m.CFSenMLCBOR || int(cf) == lwm2m.CFSenMLEtchCBOR {
		decode = lwm2m.CFSenMLCBOR
	}
	vals, err := lwm2m.Decode(decode, c.objs.Model(), lwm2m.Path{Obj: -1, Inst: -1, Res: -1, RI: -1}, readBody(r))
	if err != nil || len(vals) == 0 {
		respond(w, codes.BadRequest)
		return
	}
	for _, v := range vals {
		if v.Path.Obj == 0 || v.Path.Res < 0 || !c.objs.exists(lwm2m.Path{Obj: v.Path.Obj, Inst: v.Path.Inst, Res: -1, RI: -1}) {
			respond(w, codes.NotFound)
			return
		}
	}
	for _, v := range vals {
		if err := c.objs.SetPath(v.Path, v.V); err != nil {
			respond(w, codes.BadRequest)
			return
		}
		c.note("write %s=%v", v.Path, v.V)
	}
	respond(w, codes.Changed)
	go func() {
		for _, v := range vals {
			c.changed(v.Path)
		}
	}()
}

// decodeNames reads a SenML pack whose records may carry names only.
func decodeNames(b []byte, isCBOR bool) ([]lwm2m.Path, error) {
	if isCBOR {
		return lwm2m.DecodeSenMLNames(b, true)
	}
	return lwm2m.DecodeSenMLNames(b, false)
}
