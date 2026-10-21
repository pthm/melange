---
title: Installation
description: "Install the Melange CLI with Homebrew, Scoop, go install, a Linux package, the container image, or a prebuilt binary for Linux, macOS, or Windows."
weight: 1
prev: /docs/getting-started
next: /docs/getting-started/quick-start
---

Install the Melange CLI for schema validation, migrations, and code generation.

## Prerequisites

- **PostgreSQL 14 or later**. Melange generates PL/pgSQL functions that require PostgreSQL 14+.

## Install the CLI

{{< tabs >}}

{{< tab name="Homebrew" >}}
macOS and Linux:

```bash
brew install pthm/tap/melange
```

macOS binaries are code-signed and notarized.
{{< /tab >}}

{{< tab name="Scoop" >}}
Windows (amd64 and arm64):

```powershell
scoop bucket add pthm https://github.com/pthm/scoop-bucket
scoop install pthm/melange
```
{{< /tab >}}

{{< tab name="Go" >}}
```bash
go install github.com/pthm/melange@latest
```

Requires Go 1.27 or later. The CLI lives in the root module, so install `github.com/pthm/melange`, not `.../cmd/melange` (that path was retired in v0.9.0).
{{< /tab >}}

{{< tab name="Debian / RPM" >}}
Each release publishes `.deb` and `.rpm` packages for amd64 and arm64. The binary installs to `/usr/bin/melange`.

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/pthm/melange/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
BASE="https://github.com/pthm/melange/releases/download/v${VERSION}/melange_${VERSION}_linux_${ARCH}"

# Debian, Ubuntu
curl -fsSLO "${BASE}.deb" && sudo dpkg -i "melange_${VERSION}_linux_${ARCH}.deb"

# Fedora, RHEL
sudo rpm -U "${BASE}.rpm"
```
{{< /tab >}}

{{< tab name="Docker" >}}
A multi-arch image (linux/amd64, linux/arm64) is published to GitHub Container Registry. Tags are `latest` and `vX.Y.Z`.

```bash
docker pull ghcr.io/pthm/melange:latest
```

The image contains only the `melange` binary, so mount your project and set the working directory:

```bash
docker run --rm -v "$PWD:/work" -w /work ghcr.io/pthm/melange:latest \
  migrate --db "$DATABASE_URL" --schema schemas/schema.fga
```

If PostgreSQL runs on the host, `localhost` inside the container is the container itself. Use `--network host` on Linux or `host.docker.internal` on Docker Desktop.

Pin a version tag (for example `ghcr.io/pthm/melange:v0.10.0`) in CI so migrations are reproducible.
{{< /tab >}}

{{< tab name="Binary" >}}
Download an archive from the [GitHub releases page](https://github.com/pthm/melange/releases). Archives are named `melange_<version>_<os>_<arch>`, with `.tar.gz` for Linux and macOS and `.zip` for Windows. Each contains the `melange` binary, `LICENSE`, and `README.md`.

**Linux and macOS:**

```bash
tar -xzf melange_<version>_<os>_<arch>.tar.gz melange
sudo mv melange /usr/local/bin/
```

**Windows (PowerShell):**

```powershell
$dest = "$env:LOCALAPPDATA\Programs\melange"
Expand-Archive melange_<version>_windows_amd64.zip -DestinationPath $dest -Force
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
[Environment]::SetEnvironmentVariable("Path", "$userPath;$dest", "User")
```

Open a new terminal so the updated `Path` takes effect. Windows binaries are not code-signed yet. If the file was downloaded through a browser and Windows blocks it, run `Unblock-File "$dest\melange.exe"`.
{{< /tab >}}

{{< /tabs >}}

Verify the installation:

```bash
melange version
```

## Verifying downloads

Every release includes a `checksums.txt` file with SHA-256 hashes for each archive and package. Download it next to your file and check:

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
$expected = (Select-String -Path checksums.txt -Pattern $file).Line.Split(" ")[0]
(Get-FileHash $file -Algorithm SHA256).Hash -eq $expected.ToUpper()
```
{{< /tab >}}

{{< /tabs >}}

Homebrew and Scoop verify these hashes automatically.

Releases after v0.10.0 also carry [build provenance attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations) proving they were built by this repository's release workflow. Check an archive, package, or `checksums.txt` with the [GitHub CLI](https://cli.github.com/):

```bash
gh attestation verify melange_<version>_linux_amd64.tar.gz --repo pthm/melange
```

and the Docker image with:

```bash
gh attestation verify oci://ghcr.io/pthm/melange:v<version> --repo pthm/melange
```

## Updating

Melange checks for updates in the background and prints a notice when a newer version is available. The check is non-blocking, cached for 24 hours, and disabled in CI environments (when the `CI` environment variable is set).

To update:

{{< tabs >}}

{{< tab name="Homebrew" >}}
```bash
brew upgrade melange
```
{{< /tab >}}

{{< tab name="Scoop" >}}
```powershell
scoop update melange
```
{{< /tab >}}

{{< tab name="Go" >}}
```bash
go install github.com/pthm/melange@latest
```
{{< /tab >}}

{{< tab name="Debian / RPM" >}}
Repeat the install steps with the new version. `dpkg -i` and `rpm -U` replace the existing package.
{{< /tab >}}

{{< tab name="Docker" >}}
```bash
docker pull ghcr.io/pthm/melange:latest
```
{{< /tab >}}

{{< tab name="Binary" >}}
Download the new archive and replace the existing binary.
{{< /tab >}}

{{< /tabs >}}

To disable update notifications:

```bash
melange --no-update-check migrate
```

## Next Steps

- [Quick Start](../quick-start) to run your first permission check
- [Project Setup](../project-setup) for configuration details
