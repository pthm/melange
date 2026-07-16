# licenses

License and third-party notice management.

## Responsibility

Embeds and provides access to:

- Melange's own license text
- Third-party dependency licenses and notices

Used by the CLI to display license information and comply with open source license requirements.

## Architecture Role

```
cmd/melange
       │
       └── internal/licenses
               │
               ├── assets/LICENSE
               ├── assets/THIRD_PARTY_NOTICES
               └── third_party/ (vendored license files)
```

## Generation

Third-party notices are generated via `go generate`:

```
//go:generate go run github.com/google/go-licenses@v1.6.0 save ...
//go:generate go run gen_notice.go
```

This scans dependencies and collects their licenses into the embedded assets.

## Key Functions

- `LicenseText()` - Returns Melange's license
- `ThirdPartyText()` - Returns aggregated third-party notices
