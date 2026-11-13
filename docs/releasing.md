# Releasing Verity

Development and releases use GitHub at `pgodlews/verity-api`. Verity is alpha;
`0.x` releases may contain breaking changes. Update [CHANGELOG.md](../CHANGELOG.md)
and ensure the release commit has green CI before tagging it.

## Tags and workflow

Pushing a `v*` tag triggers
[`.github/workflows/release.yml`](../.github/workflows/release.yml). Use SemVer
(`vMAJOR.MINOR.PATCH`), with a suffix such as `-rc.1` for a pre-release.
For example, after choosing an unused version:

```bash
git switch main
git pull --ff-only
git tag -a "$VERSION" -m "Verity $VERSION"
git push origin "$VERSION"
```

The public release pipeline is:

0. **Tests:** a `test` job vets and tests the core/CLI, gRPC plugin, and
   desktop backend modules and runs the frontend tests and type check.
   Nothing is built unless it passes.
1. **CLI and gRPC plugin archives:** `scripts/release.sh` builds `verity` and
   `verity-grpc` for linux/darwin/windows on amd64/arm64 (six targets each).
   Archives contain the binary, README, and MIT license; Unix archives use
   `.tar.gz`, Windows archives `.zip`. Builds disable CGO and use `-trimpath`;
   CLI metadata includes the version, commit, and commit date.
2. **Desktop apps:** Linux amd64 with WebKitGTK 4.1, universal macOS
   (Intel and Apple silicon), and Windows amd64. `scripts/desktop-version.sh`
   sets `info.productVersion` in `gui/wails.json` from the tag and supplies
   the `-X main.version/commit/date` ldflags. Asset names are
   `verity-desktop_<tag>_linux_amd64.tar.gz`,
   `verity-desktop_<tag>_darwin_universal.zip`, and
   `verity-desktop_<tag>_windows_amd64.zip`.
3. **Checksums and publication:** attach the archives and `SHA256SUMS` to a
   GitHub Release. Check archive hashes before publication and include
   desktop bundles in the final checksum file. Re-running the job after a
   partial failure replaces the assets of the existing release
   (`gh release upload --clobber`) instead of failing.
4. **Container image:** after the release exists, `scripts/docker-image.sh`
   uses `crane` to assemble and push `ghcr.io/pgodlews/verity:<tag>` for
   linux/amd64 and linux/arm64. Tags without a pre-release suffix also update
   `:latest`, so a failed desktop build or release never moves `:latest`.
5. **Homebrew:** for non-pre-releases, `scripts/homebrew-formula.sh` generates
   `Formula/verity.rb` with GitHub archive URLs and checksums, then the workflow
   updates the `pgodlews/homebrew-verity` tap.

A tag containing `-` is published as a GitHub pre-release, skips the Homebrew
tap update, and does not move the image's `:latest` tag. Consumers must use
its explicit version tag.

## GitHub configuration

The release workflow's `GITHUB_TOKEN` is read-only by default; only the
`publish` job gets `contents: write` (the release) and only the `image` job
gets `packages: write` (GHCR).
The tap update needs a separate Actions secret named `HOMEBREW_TAP_TOKEN`:
a fine-grained GitHub token limited to `pgodlews/homebrew-verity`, with
repository **Contents: read and write** permission (`contents: write`).
Keep the token in GitHub Actions secrets, never in files or command examples.
The tap job runs only for tags without a pre-release suffix. If the secret is
missing, the workflow warns and skips the tap update; the release still publishes.

## Install and verify

Download an archive and `SHA256SUMS` from
[GitHub Releases](https://github.com/pgodlews/verity-api/releases).
Verify the archive before extracting it:

```bash
# Linux: verify the downloaded archives; missing platforms are ignored.
sha256sum -c SHA256SUMS --ignore-missing
# macOS: compare this hash with the archive's entry in SHA256SUMS.
shasum -a 256 "$ARCHIVE"
```

On Windows, use PowerShell `Get-FileHash -Algorithm SHA256 $Archive` and compare
with the matching checksum entry. After extracting the CLI, run
`verity version --json`. Register the gRPC plugin separately as described in
[its guide](../plugins/grpc/README.md).

```bash
brew install pgodlews/verity/verity
docker run --rm -v "$PWD:/work" -w /work --user "$(id -u):$(id -g)" ghcr.io/pgodlews/verity:latest run project.yaml
```

The Linux desktop app needs the WebKitGTK 4.1 runtime. Windows needs WebView2.
Desktop alpha builds are unsigned; macOS builds are not notarized:

- macOS: after verifying the archive and trying to launch, use **System Settings → Privacy & Security → Open Anyway** for the blocked app.
- Windows: after verifying the archive, choose **More info → Run anyway** in SmartScreen.

## Local archive build

On Linux with Go 1.26+, Git, and archive/checksum utilities:

```bash
scripts/release.sh v0.0.0-local dist/release
(cd dist/release && sha256sum -c SHA256SUMS)
```

This builds CLI/plugin archives only. See [the desktop guide](../gui/README.md)
for Wails builds. Code signing and notarization are not automated.
