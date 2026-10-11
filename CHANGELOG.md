# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.2.0 (2026-10-12)

Needs lwm2m v0.2.0.

- The device logic moved to the lwm2m module's `client` package (the
  LwM2M client role as a library); `sim.Config`, `sim.Client` and
  `sim.Objects` are aliases of its types and `sim.New`, `NewObjects`
  and `Default` call it, so v0.1.0 code builds unchanged.
- DTLS certificate mode through `Config.Certificate`, `RootCAs` and
  `ServerName`.
- Interop rerun on the moved code: Leshan 2.0.0-M18's demo server
  (registration, reads, writes, observations in every format, DTLS PSK)
  and bootstrap server against the simulator, passed (2026-10-12).

## v0.1.0 (2026-10-11)

First release: a simulated LwM2M client device (`sim.New`, `Client`,
`Objects`, `Default`) over CoAP/UDP and CoAP/DTLS with a pre-shared key,
with registration and updates, client-initiated bootstrap, Read,
Discover, Write, Write-Attributes, Execute, Create, Delete,
Read-Composite, Write-Composite, Observe, Cancel, Notify and Send in
every content format of github.com/cB-Sigma/lwm2m, and `cmd/lwm2msim`.
Interop tests against Eclipse Leshan 2.0.0-M18's demo server and
bootstrap server in a nested test-only module, `interop`.
