# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.1.0 (2026-10-11)

First release: a simulated LwM2M client device (`sim.New`, `Client`,
`Objects`, `Default`) over CoAP/UDP and CoAP/DTLS with a pre-shared key,
with registration and updates, client-initiated bootstrap, Read,
Discover, Write, Write-Attributes, Execute, Create, Delete,
Read-Composite, Write-Composite, Observe, Cancel, Notify and Send in
every content format of github.com/cB-Sigma/lwm2m, and `cmd/lwm2msim`.
Interop tests against Eclipse Leshan 2.0.0-M18's demo server and
bootstrap server in a nested test-only module, `interop`.
