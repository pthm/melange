---
title: Installation
description: "Install the Melange CLI on macOS, Linux, or Windows with Homebrew, Scoop, a Linux package, go install, or the container image, and add the Go or TypeScript client library."
weight: 1
prev: /docs/getting-started
next: /docs/getting-started/quick-start
---

The `melange` CLI validates schemas, migrates them into PostgreSQL, and generates client code. It is a single binary with no runtime dependencies. You only need PostgreSQL 14 or later when you run `melange migrate`. To call Melange from your application, also add a [client library](#client-libraries).

Find your platform below and pick one install method. Each release is published for amd64 and arm64 on [GitHub Releases](https://github.com/pthm/melange/releases). Once installed, check it works:

```bash
melange version
```

## macOS

### Homebrew (recommended)

```bash
brew install pthm/tap/melange
```

### Archive

Download `melange_<version>_darwin_<arch>.tar.gz` from the [latest release](https://github.com/pthm/melange/releases/latest) (`arm64` for Apple silicon, `amd64` for Intel), then:

```bash
tar -xzf melange_<version>_darwin_<arch>.tar.gz melange
sudo mv melange /usr/local/bin/
```

macOS binaries are code-signed and notarized.

## Linux

### Homebrew

```bash
brew install pthm/tap/melange
```

### Debian / Ubuntu (`.deb`)

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/pthm/melange/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
curl -fsSLo /tmp/melange.deb "https://github.com/pthm/melange/releases/download/v${VERSION}/melange_${VERSION}_linux_$(dpkg --print-architecture).deb"
sudo dpkg -i /tmp/melange.deb
```

### Fedora / RHEL (`.rpm`)

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/pthm/melange/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
sudo rpm -U "https://github.com/pthm/melange/releases/download/v${VERSION}/melange_${VERSION}_linux_${ARCH}.rpm"
```

Both packages install the binary to `/usr/bin/melange`.

### Archive

Download `melange_<version>_linux_<arch>.tar.gz` from the [latest release](https://github.com/pthm/melange/releases/latest), then:

```bash
tar -xzf melange_<version>_linux_<arch>.tar.gz melange
sudo mv melange /usr/local/bin/
```

## Windows

### Scoop (recommended)

With [Scoop](https://scoop.sh) installed:

```powershell
scoop bucket add pthm https://github.com/pthm/scoop-bucket
scoop install pthm/melange
```

### Archive

Download `melange_<version>_windows_<arch>.zip` from the [latest release](https://github.com/pthm/melange/releases/latest), then extract it and add the folder to your user `Path` in PowerShell:

```powershell
$dest = "$env:LOCALAPPDATA\Programs\melange"
Expand-Archive melange_<version>_windows_amd64.zip -DestinationPath $dest -Force
$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (($path -split ";") -notcontains $dest) {
  [Environment]::SetEnvironmentVariable("Path", "$path;$dest", "User")
}
```

Open a new terminal for the `Path` change to apply. Windows binaries are not code-signed yet; if Windows blocks a browser-downloaded binary, run `Unblock-File "$dest\melange.exe"`.

## Any platform

### Go

```bash
go install github.com/pthm/melange@latest
```

Requires Go 1.27 or later. Install `github.com/pthm/melange`, not `.../cmd/melange`; that path was retired in v0.9.0.

### Docker

```bash
docker pull ghcr.io/pthm/melange:latest
```

The image (linux/amd64 and linux/arm64) contains only the binary. Mount your project and run from it:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" -w /work \
  ghcr.io/pthm/melange:v0.10.0 migrate --db "$DATABASE_URL" --schema schemas/schema.fga
```

- **`--user`**: the image runs as root, so without it, files written into the mount on Linux are owned by root. Docker Desktop on macOS and Windows doesn't need it.
- **Database on the host**: `localhost` inside the container is the container itself. Use `--network host` on Linux or `host.docker.internal` on Docker Desktop.
- **Tags**: `vX.Y.Z` for an exact release, `vX.Y` to follow its patch releases, and `latest`. Pin `vX.Y.Z` in CI so migrations are reproducible.

## Client libraries

Your application calls the generated permission functions through a client library. [`melange init`](../project-setup#client-code-generation) adds it for you when it sets up code generation; to add it yourself:

### Go

```bash
go get github.com/pthm/melange/melange
```

Requires Go 1.24 or later. The library uses only the standard library.

### TypeScript

```bash
npm install @pthm/melange pg
```

Requires Node.js 18 or later. [`pg`](https://www.npmjs.com/package/pg) (version 8) is a peer dependency. Use your package manager's equivalent (`pnpm add`, `yarn add`, `bun add`) if you don't use npm.

## Updating

When a newer release is out, the CLI prints a notice with the update command for how you installed it.

| Installed with | Update with |
|---|---|
| Homebrew | `brew upgrade pthm/tap/melange` |
| Scoop | `scoop update melange` |
| `.deb` / `.rpm` | Repeat the install commands. They replace the existing package. |
| Archive | Download the new archive and replace the binary. |
| Go | `go install github.com/pthm/melange@latest` |
| Docker | `docker pull ghcr.io/pthm/melange:latest` |

The check runs in the background, is cached for 24 hours, and is off when the `CI` environment variable is set. To skip it for one command, pass `--no-update-check`.

## Verifying downloads

Homebrew, Scoop, and `go install` verify downloads for you. For a package or archive you downloaded yourself, check it against the release's `checksums.txt`, saved in the same folder:

{{< tabs >}}

{{< tab name="Linux" >}}
```bash
sha256sum --ignore-missing -c checksums.txt
```
{{< /tab >}}

{{< tab name="macOS" >}}
```bash
shasum -a 256 --ignore-missing -c checksums.txt
```
{{< /tab >}}

{{< tab name="Windows" >}}
```powershell
$file = "melange_<version>_windows_amd64.zip"
$expected = (Select-String -Path checksums.txt -Pattern $file -SimpleMatch).Line.Split(" ")[0]
(Get-FileHash $file -Algorithm SHA256).Hash -eq $expected.ToUpper()
```
{{< /tab >}}

{{< /tabs >}}

Releases after v0.10.0 also carry [build provenance attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations), which prove a file or image was built by this repository's release workflow. Check them with the [GitHub CLI](https://cli.github.com/):

```bash
gh attestation verify melange_<version>_linux_amd64.tar.gz --repo pthm/melange
gh attestation verify oci://ghcr.io/pthm/melange:v<version> --repo pthm/melange
```

## Next steps

- [Quick Start](../quick-start) to run your first permission check
- [Project Setup](../project-setup) for configuration details
