// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"

	"github.com/cB-Sigma/lwm2m"
)

// bootstrapping tells whether a bootstrap is in progress (between the
// Bootstrap-Request and Bootstrap-Finish).
func (c *Client) bootstrapping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.booted != nil
}

// bootstrap runs the client-initiated bootstrap (Core clause 5.2.7.1):
// Bootstrap-Request to the bootstrap server, then the server's
// Bootstrap-Discover, -Read, -Delete and -Write requests until
// Bootstrap-Finish. It returns the LwM2M server the bootstrap server
// provisioned (the first non-bootstrap Security instance) with its PSK.
func (c *Client) bootstrap(ctx context.Context) (server, id string, key []byte, err error) {
	finished := make(chan struct{})
	c.mu.Lock()
	c.booted = finished
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.booted = nil
		c.mu.Unlock()
	}()
	if err := c.connect(c.cfg.Bootstrap, c.cfg.PSKIdentity, c.cfg.PSK); err != nil {
		return "", "", nil, err
	}
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resp, err := cc.Post(rctx, "/bs", message.TextPlain, nil, message.Option{ID: message.URIQuery, Value: []byte("ep=" + c.cfg.Endpoint)})
	cancel()
	if err != nil {
		return "", "", nil, fmt.Errorf("lwm2m-sim: bootstrap request: %w", err)
	}
	if resp.Code() != codes.Changed {
		return "", "", nil, fmt.Errorf("lwm2m-sim: bootstrap request: %v", resp.Code())
	}
	select {
	case <-finished:
	case <-ctx.Done():
		return "", "", nil, ctx.Err()
	case <-time.After(60 * time.Second):
		return "", "", nil, errors.New("lwm2m-sim: no Bootstrap-Finish within 60 s")
	}
	return c.provisioned()
}

// provisioned reads the LwM2M server account the bootstrap wrote.
func (c *Client) provisioned() (server, id string, key []byte, err error) {
	for _, k := range c.objs.instances() {
		if k[0] != 0 {
			continue
		}
		base := "/0/" + strconv.Itoa(k[1]) + "/"
		if bs, _ := c.objs.Get(base + "1"); bs == true {
			continue
		}
		uri, _ := c.objs.Get(base + "0")
		s, _ := uri.(string)
		if s == "" {
			continue
		}
		mode, _ := c.objs.Get(base + "2")
		if m, _ := mode.(float64); m == 0 && strings.HasPrefix(s, "coaps://") {
			idHex, _ := c.objs.Get(base + "3")
			keyHex, _ := c.objs.Get(base + "5")
			idb, _ := hex.DecodeString(fmt.Sprint(idHex))
			kb, _ := hex.DecodeString(fmt.Sprint(keyHex))
			id, key = string(idb), kb
		}
		// The registration lifetime comes from the Server instance with
		// the same short server ID.
		if ssid, ok := c.objs.Get(base + "10"); ok {
			for _, s1 := range c.objs.instances() {
				if s1[0] != 1 {
					continue
				}
				b1 := "/1/" + strconv.Itoa(s1[1]) + "/"
				if v, _ := c.objs.Get(b1 + "0"); v == ssid {
					if lt, ok := c.objs.Get(b1 + "1"); ok {
						if f, _ := lt.(float64); f > 0 {
							c.cfg.Lifetime = time.Duration(f) * time.Second
						}
					}
				}
			}
		}
		return s, id, key, nil
	}
	return "", "", nil, errors.New("lwm2m-sim: the bootstrap wrote no LwM2M server account")
}

// bootstrapWrite stores a Bootstrap-Write; any object and instance may be
// written, and instances are created as needed.
func (c *Client) bootstrapWrite(w mux.ResponseWriter, r *mux.Message, p lwm2m.Path) {
	if p.Inst < 0 {
		// A write on an object carries its instances.
		p.Inst = -1
	}
	vals, _, err := c.decodeWrite(r, p)
	if errors.Is(err, errUnsupported) {
		respond(w, codes.UnsupportedMediaType)
		return
	}
	if err != nil {
		c.log.Warn("lwm2m-sim: bootstrap write", "path", p, "err", err)
		respond(w, codes.BadRequest)
		return
	}
	for _, v := range vals {
		if v.Path.Inst < 0 || v.Path.Res < 0 {
			respond(w, codes.BadRequest)
			return
		}
		if err := c.objs.SetPath(v.Path, v.V); err != nil {
			c.log.Warn("lwm2m-sim: bootstrap write", "path", v.Path, "err", err)
			respond(w, codes.BadRequest)
			return
		}
	}
	c.note("bootstrap-write %s", p)
	respond(w, codes.Changed)
}

// bootstrapDelete deletes an object or instance; "/" deletes the
// Security instances other than the bootstrap server's and the Server
// instances, which is what a bootstrap server replaces.
func (c *Client) bootstrapDelete(w mux.ResponseWriter, p lwm2m.Path) {
	if !c.bootstrapping() {
		respond(w, codes.MethodNotAllowed)
		return
	}
	if p.Obj < 0 {
		for _, k := range c.objs.instances() {
			if k[0] == 0 {
				if bs, _ := c.objs.Get("/0/" + strconv.Itoa(k[1]) + "/1"); bs == true {
					continue
				}
			}
			if k[0] == 0 || k[0] == 1 {
				c.objs.deleteUnder(lwm2m.Path{Obj: k[0], Inst: k[1], Res: -1, RI: -1})
			}
		}
		c.note("bootstrap-delete /")
		respond(w, codes.Deleted)
		return
	}
	if p.Obj == 3 || p.Res >= 0 {
		respond(w, codes.BadRequest)
		return
	}
	c.objs.deleteUnder(p)
	c.note("bootstrap-delete %s", p)
	respond(w, codes.Deleted)
}

// bootstrapFinish ends the bootstrap.
func (c *Client) bootstrapFinish(w mux.ResponseWriter) {
	c.mu.Lock()
	ch := c.booted
	c.mu.Unlock()
	if ch == nil {
		respond(w, codes.MethodNotAllowed)
		return
	}
	if _, _, _, err := c.provisioned(); err != nil {
		// Bootstrap-Finish fails with 4.06 when the configuration is not
		// usable (Core clause 5.2.7.4).
		respond(w, codes.NotAcceptable)
		return
	}
	c.note("bootstrap-finish")
	respond(w, codes.Changed)
	select {
	case <-ch:
	default:
		close(ch)
	}
}
