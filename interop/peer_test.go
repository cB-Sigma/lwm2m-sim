// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// image is the Leshan peer built from peer/Dockerfile.
const image = "sigma-lwm2m-leshan:2.0.0-M18"

// restClient bounds every REST call; the event stream uses the default client.
var restClient = &http.Client{Timeout: 30 * time.Second}

var (
	buildOnce sync.Once
	buildErr  error
)

// needDocker skips the test unless SIGMA_LWM2M_INTEROP=1 is set and
// Docker answers, and builds the peer image (from cache after the first
// run).
func needDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("SIGMA_LWM2M_INTEROP") != "1" {
		t.Skip("set SIGMA_LWM2M_INTEROP=1 to run the Leshan interop tests (needs Docker)")
	}
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker does not answer: %v", err)
	}
	buildOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		out, err := exec.Command("docker", "build", "-q", "-t", image, filepath.Join(filepath.Dir(file), "peer")).CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("docker build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}

// leshan is a running Leshan demo server (or bootstrap server).
type leshan struct {
	name  string
	web   string // http://127.0.0.1:port
	coap  string // 127.0.0.1:port
	coaps string // 127.0.0.1:port
}

func containerName(t *testing.T, role string) string {
	return "sigma-lwm2m-sim-interop-" + role + "-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
}

// startLeshan runs jar (server.jar or bsserver.jar) with its CoAP, CoAPS
// and web ports published on ephemeral loopback ports.
func startLeshan(t *testing.T, role, jar string, extra ...string) *leshan {
	t.Helper()
	needDocker(t)
	l := &leshan{name: containerName(t, role)}
	args := append([]string{"run", "--rm", "-d", "--name", l.name,
		"-p", "127.0.0.1::5683/udp", "-p", "127.0.0.1::5684/udp", "-p", "127.0.0.1::8080/tcp",
		image, "java", "-jar", jar, "-lp", "5683", "-slp", "5684", "-wp", "8080"}, extra...)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", "--tail", "80", l.name).CombinedOutput()
			t.Logf("%s logs:\n%s", l.name, out)
		}
		_ = exec.Command("docker", "stop", "-t", "2", l.name).Run()
	})
	port := func(p string) string {
		out, err := exec.Command("docker", "port", l.name, p).Output()
		if err != nil {
			t.Fatalf("docker port %s: %v", p, err)
		}
		return strings.TrimSpace(strings.Split(string(out), "\n")[0])
	}
	l.coap, l.coaps, l.web = port("5683/udp"), port("5684/udp"), "http://"+port("8080/tcp")
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(l.web + "/")
		if err == nil {
			resp.Body.Close()
			// The CoAP endpoints start before the web server.
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("Leshan's web server did not answer: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// do sends a REST request to the Leshan demo API and decodes the JSON
// answer into out (when not nil); it returns the HTTP status.
func (l *leshan) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, l.web+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := restClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	if resp.StatusCode >= 300 {
		t.Logf("%s %s = %d %s", method, path, resp.StatusCode, b)
	}
	return resp.StatusCode
}

// waitRegistered waits until Leshan lists ep and returns its registration.
func (l *leshan) waitRegistered(t *testing.T, ep string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var reg map[string]any
		req, _ := http.NewRequest(http.MethodGet, l.web+"/api/clients/"+ep, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && json.Unmarshal(b, &reg) == nil {
				return reg
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not register", ep)
	return nil
}

// event is one server-sent event of the demo server.
type event struct {
	kind string
	data map[string]any
}

// events subscribes to the demo server's event stream for ep.
func (l *leshan) events(t *testing.T, ep string) <-chan event {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, l.web+"/api/event?ep="+ep, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan event, 64)
	t.Cleanup(func() { resp.Body.Close() })
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var kind string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				kind = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				var d map[string]any
				if (kind == "NOTIFICATION" || kind == "SEND") && json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &d) == nil {
					select {
					case ch <- event{kind, d}:
					default:
					}
				}
			}
		}
	}()
	return ch
}

// next waits for the next event of kind.
func next(t *testing.T, ch <-chan event, kind string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case e := <-ch:
			if e.kind == kind {
				return e.data
			}
		case <-deadline:
			t.Fatalf("no %s event within %v", kind, within)
			return nil
		}
	}
}
