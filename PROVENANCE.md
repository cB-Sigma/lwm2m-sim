# Provenance

## How the code was written

Written in Go on 2026-10-11 for this module, from the specifications
below, to give the lwm2m codecs a client of their own and to replace ad
hoc test clients. Until then SigmaM2M's LwM2M server tests used a test
client inside its own test package; this module is new code, not that
fixture moved. No code was ported or copied from another LwM2M
implementation; Eclipse Leshan is used only as a test peer, in a
container, through its demo server's REST API.

## Sources

| Area (files) | Followed |
|---|---|
| Registration, update, de-registration (sim.go) | OMA-TS-LightweightM2M_Core V1.1.1 clause 6.3 and Transport V1.1.1 clause 6.4 |
| Device management and information reporting (sim.go) | Core 1.1.1 clauses 6.3 to 6.4 and 5.4 (Read, Discover, Write, Write-Attributes, Execute, Create, Delete, Read-Composite, Write-Composite, Observe, Cancel, Notify, Send) |
| Bootstrap (bootstrap.go) | Core 1.1.1 clause 5.2.7 (client-initiated bootstrap, Bootstrap-Discover, -Write, -Delete, -Finish) and appendix E.1 for the Security object |
| CoAP, FETCH and iPATCH, DTLS PSK | RFC 7252, RFC 7641, RFC 8132, RFC 6347 through go-coap and pion/dtls |
| Content formats | github.com/cB-Sigma/lwm2m |

## Simplifications (in our own words)

- A Bootstrap-Delete of "/" removes the Security instances other than the
  bootstrap server's and every Server instance; the client's other
  objects stay, so that a test device keeps its data.
- The object model gives types, not access rights: every resource that
  is not executable can be written.
- Write-Attributes are stored and shown by Discover; notifications are
  sent on every change, pmin and pmax are not applied.

## Test data

Every endpoint name, resource value, key and address is a made-up demo
value. The interop tests compare with values Leshan's demo programs
report about themselves.
