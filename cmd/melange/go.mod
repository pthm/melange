// Deprecated: the melange CLI moved into the root module.
// Install it with: go install github.com/pthm/melange@latest
module github.com/pthm/melange/cmd/melange

go 1.25.7

// This module is retired. Its only remaining code is an error-shim main that
// points at the new install path. Every prior release (v0.1.0–v0.8.6, all 16
// published tags) is retracted so `go install .../cmd/melange@latest` resolves
// to this shim — which is published at a higher version — and never to a frozen
// pre-migration CLI. Retracting the full range (not just the last few) also
// hides the old versions from `go list -m -versions`.
retract [v0.1.0, v0.8.6] // CLI merged into the root module; install github.com/pthm/melange@latest
