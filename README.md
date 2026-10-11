# lwm2m-sim

A simulated OMA LightweightM2M client device in pure Go, over CoAP/UDP and CoAP/DTLS with a pre-shared key, for testing LwM2M servers without hardware; not affiliated with OMA SpecWorks.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/lwm2m-sim.svg)](https://pkg.go.dev/github.com/cB-Sigma/lwm2m-sim) [![test](https://github.com/cB-Sigma/lwm2m-sim/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/lwm2m-sim/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)

The simulated device registers with an LwM2M server, or bootstraps first,
keeps its registration up to date and answers the server in every
content format: reads, writes, executes, observations with
notifications, composite operations and Send. Its objects live in an
`Objects` store the test can read and change. Since v0.2.0 the device
logic is the client role of [lwm2m](https://github.com/cB-Sigma/lwm2m)
(package `client`); this module keeps the simulator's names, its demo
objects, the command and the tests that drive it. No cgo. Written for SigmaM2M, a oneM2M platform; it has no dependency on it.

## What is simulated

| Feature | Status |
|---|---|
| Register, Update (every half lifetime, and on request), De-register; a 4.04 to an Update registers again | yes |
| CoAP over UDP; CoAP over DTLS 1.2 with a PSK (AES-128-CCM-8, AES-128-GCM) or, since v0.2.0, an X.509 certificate (ECDHE-ECDSA) | yes |
| Client-initiated bootstrap: Bootstrap-Request, -Discover, -Read, -Write, -Delete, -Finish, then registration with the provisioned server, PSK and lifetime | yes |
| Read in TLV, SenML JSON and CBOR, LwM2M JSON and CBOR; single resources also in plain text, CBOR and opaque; 4.06 for others | yes |
| Discover with attributes and `dim` | yes |
| Write (replace, partial update), Write-Attributes, Execute (with a handler per path), Create, Delete | yes |
| Read-Composite and Write-Composite (FETCH, iPATCH), Observe-Composite | yes |
| Observe and Cancel (GET with Observe 1, or a reset), Notify on every change in the observer's format | yes |
| Send (POST /dp, SenML CBOR) | yes |
| Refusals: Security object closed to the server (4.01), executable resources not readable or writable (4.05), unknown paths (4.04) | yes |
| Default objects: Server (1), Device (3), Connectivity Monitoring (4), Temperature (3303); your own through `Objects` | yes |
| pmin/pmax applied to notifications, queue mode, SMS, CoAP over TCP, OSCORE, raw public keys, firmware update | no |

## Choosing an LwM2M test client

| | this module | Leshan demo client 2.0.0-M18 (Java) | zourva/lwm2m v0.2.0 client (Go) |
|---|---|---|---|
| In-process in Go tests | yes | no (a JVM, a container) | yes |
| Content formats | TLV, text, opaque, CBOR, SenML JSON/CBOR, LwM2M JSON/CBOR | TLV, text, opaque, CBOR, SenML JSON/CBOR, LwM2M JSON (no LwM2M CBOR: it answers 4.06) | TLV |
| Values the test sets and reads back | yes (`Objects`, `Set`, `Record`) | no (random demo sensors, a console) | partly |
| Bootstrap, DTLS PSK | yes | yes, also RPK, X.509, OSCORE | DTLS |
| Licence | Apache-2.0 | EPL-2.0 / EDL-1.0 | MIT |

Pick this module for Go tests that need a device whose values the test
controls and checks. Pick Leshan's client (or Wakaama's or Anjay's in C) when you need an
independent implementation, certificates, OSCORE or a reference
behaviour: the tests here use Leshan for exactly that.

## Install

```
go get github.com/cB-Sigma/lwm2m-sim@latest
```

Requires Go 1.26 or later. The command:

```
go install github.com/cB-Sigma/lwm2m-sim/cmd/lwm2msim@latest
```

## Command line

```
lwm2msim -server coap://127.0.0.1:5683 -ep demo-1
lwm2msim -server coaps://127.0.0.1:5684 -ep demo-1 -psk-id demo-1 -psk 000102030405060708090a0b0c0d0e0f
lwm2msim -bootstrap coap://127.0.0.1:5783 -ep demo-1
```

| Flag | Default | Meaning |
|---|---|---|
| `-server` | | LwM2M server, `coap://host:port` or `coaps://host:port` |
| `-bootstrap` | | bootstrap server; the client bootstraps first and `-server` is not used |
| `-ep` | `lwm2m-sim` | endpoint name |
| `-psk-id`, `-psk` | | DTLS PSK identity and key (hex) for coaps |
| `-lifetime` | `5m` | registration lifetime |
| `-version` | `1.1` | LwM2M version registered: 1.0, 1.1 or 1.2 |
| `-interval` | `5s` | how often the temperature changes (0: never) |
| `-v` | | log every request |

## In Go tests

From `Example` in sim/example_test.go:

```go
c := sim.New(sim.Config{Endpoint: "demo-1", Server: "coap://127.0.0.1:5683", Lifetime: 5 * time.Minute})
if err := c.Start(ctx); err != nil { ... }
defer c.Stop()
_ = c.Set("/3303/0/5700", 23.5)   // observers get a notification
_ = c.Send(ctx, "/3303/0/5700")   // LwM2M 1.1 Send
fmt.Println(c.Record())           // writes, executes, creates, ... the server did
```

## Documentation

Reference: https://pkg.go.dev/github.com/cB-Sigma/lwm2m-sim

| Package | What it does |
|---|---|
| `sim` | `New(Config)`, `Client` (`Start`, `Stop`, `Update`, `Set`, `Send`, `OnExecute`, `Record`, `Observations`, `Location`, `Objects`), `Objects` (`NewObjects`, `Default`, `Set`, `Get`, `Values`, `AddExecutable`) |
| `cmd/lwm2msim` | Runs one simulated device with a drifting temperature |
| `interop` (nested module, tests only) | the simulator against Eclipse Leshan's demo server and bootstrap server, in a container |

Changes per version are in [CHANGELOG.md](CHANGELOG.md).

## Testing

```
go test -race ./...
cd interop && SIGMA_LWM2M_INTEROP=1 go test -v ./...   # needs Docker
```

The tests in `sim` drive the simulator from a small LwM2M server and
bootstrap server written in the test with go-coap and pion/dtls: every
read and write format, refusals, Discover, Create and Delete,
observations and cancel, Read-Composite, Send, DTLS with a right and a
wrong key, and a bootstrap.

### Interop tested against

- Eclipse Leshan 2.0.0-M18 demo server (2026-10-11, Docker Desktop on
  arm64; `interop`), through its REST API: the simulator registered
  (version, lifetime and objects as sent); Leshan read `/3/0`, `/3303/0`,
  `/4/0`, `/1/0` and single resources in TLV, SenML JSON, SenML CBOR and
  LwM2M JSON, and single resources in plain text and CBOR, and every
  value it decoded equals the simulator's; Leshan wrote a string, an
  integer, a boolean and a float in each of those six formats and the
  simulator stored each; a partial update of an instance, Write-Attributes
  (shown back by Discover), Execute, a refused execute (4.05), a refused
  read of the Security object (4.01), Create and Delete of a 3303
  instance, and Read-Composite in SenML JSON and CBOR all behaved as
  Leshan expects. Leshan observed `/3303/0/5700` and `/3303/0` in each
  format and received three notifications each, with the values set;
  Leshan received a Send. Over DTLS with a PSK registered in Leshan the
  simulator registered and was read; a wrong key did not register.
- Eclipse Leshan 2.0.0-M18 demo bootstrap server: the simulator
  bootstrapped (Bootstrap-Delete of /0 and /1, Bootstrap-Write of two
  Security instances and a Server instance, Bootstrap-Finish), then
  registered with the Leshan server it was given, with the lifetime of
  the Server instance.
- The first run found that whole-number values of Float resources went
  out as CBOR integers, which Leshan refuses; fixed in lwm2m v0.1.0.
- LwM2M CBOR (11544) is not checked against Leshan, which does not
  implement it. Not yet: Wakaama, Anjay, a commercial server.
- The simulator and the lwm2m codecs are written by the same author, so
  their agreement is not independent; the Leshan runs are.

## Demo values

Every endpoint name, value, key and address in this module is a made-up
demo value. Do not use the keys for real devices.

## Related repositories

- [lwm2m](https://github.com/cB-Sigma/lwm2m): the content formats and
  object model this module is built on.

## Status

v0.x: the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. LightweightM2M and LwM2M are names of OMA SpecWorks,
used only to say what this module simulates.
