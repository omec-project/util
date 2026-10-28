<!--
Copyright 2024-present Intel Corporation
SPDX-License-Identifier: Apache-2.0
-->
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/omec-project/util/badge)](https://scorecard.dev/viewer/?uri=github.com/omec-project/util)
[![GitHub latest release (SemVer)](https://img.shields.io/github/v/release/omec-project/util?sort=semver)](https://github.com/omec-project/util/releases)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://spdx.org/licenses/Apache-2.0.html)

# util

Shared Go libraries used by the network functions of the
[Aether SD-Core](https://github.com/omec-project) 5G core. The repository is a
single Go module, `github.com/omec-project/util`; import only the packages you need.

## Packages

| Package | Purpose |
|---|---|
| [`drsm`](drsm/README.md) | Distributed Resource Sharing Module: MongoDB-backed allocation of unique ID chunks across multiple instances of a service (e.g. AMF NGAP IDs) |
| `flowdesc` | Encoding of IPFilterRule flow descriptions |
| `fsm` | Finite state machine with transitions and callbacks, plus Graphviz DOT export |
| `http2_util` | Construction of HTTP/2 `http.Server` instances (TLS key logging, error log handling) |
| `httpwrapper` | Lightweight `Request` / `Response` wrappers around `net/http` |
| `idgenerator` | Thread-safe allocator of IDs within a `[min, max]` range |
| `logger` | zap-based logging helpers, log-level settings and a gin engine wired to zap |
| `metricinfo` | Data types for core subscriber, NF status and metric events |
| `milenage` | Milenage authentication functions (F1, F2345, OPc generation) |
| `mongoapi` | MongoDB client wrapper and index management helpers |
| `ueauth` | Key derivation function (KDF) helpers for UE authentication |

## Requirements

Go 1.26.0 or later (see `go.mod`).

## Development

```bash
go test ./...
```

Linting and checks are configured in `.pre-commit-config.yaml` (gitleaks, gci,
staticcheck, `go test -race`, golangci-lint, yamlfmt). Run them with
`pre-commit run --all-files`.

## Security

See [docs/SECURITY.md](docs/SECURITY.md).

## License

Apache-2.0. See [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt).
