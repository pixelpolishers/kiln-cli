# Changelog

All notable changes to Kiln (the `kiln` command line tool) are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- This changelog, and a copy of it in every release archive.
- Prebuilt binaries for Windows on ARM (arm64) and FreeBSD (amd64).
- Tests on Linux and Windows machines with ARM processors for every change.

### Changed

- The text of a GitHub release is now the section of its version in this file.

## [0.1.0-rc1] - 2026-10-20

The first release candidate: a working prototype. Libraries are built from their upstream git tags on your machine; there are no prebuilt libraries yet. Limitations to know about: the registry that serves the packages is the separate `kiln-website` repository, and its demo packages are not published yet; the binaries are not signed or notarized, and the macOS, Windows and arm64 binaries have not been run by hand; on Windows only MinGW-w64 GCC with Ninja is tested, MSVC is not.

### Added

- Declare dependencies in `kiln.json` and install them with `kiln install`. Everybody who uses the same `kiln.lock` gets the same versions, on every operating system.
- Commands to find, add and remove packages: `search`, `info`, `resolve`, `init`, `require` and `remove`.
- Commands to keep dependencies healthy: `update`, `outdated`, `why`, `why-not`, `audit` (known vulnerabilities, from osv.dev) and `licenses` (list the licenses of the locked packages and check them against an allow or deny policy).
- Building libraries from source with CMake, or with the custom steps of a manifest, using the build settings detected on your machine. Results are cached under an exact build identity, and `kiln cache ls` and `kiln cache clear` manage the cache.
- System requirements: libraries state which tools and development packages they need, per operating system and Linux distribution, and `kiln install --check` reports what is missing, with install hints, before anything is built.
- A `vendor/kiln/kiln.cmake` file for your CMake project, a copy of the library sources under `vendor/` for reading and debugging, and `find_package` support for libraries that ship their own CMake configuration.
- Build programs that libraries provide (code generators such as `protoc`) as imported CMake executables.
- `package`, `publish` and `finalize` to build a local repository of prebuilt packages.
- Prebuilt `kiln` binaries for Linux (amd64, arm64), macOS (arm64, amd64) and Windows (amd64), with a `SHA256SUMS` file.
- Tests on Ubuntu, Fedora, macOS and Windows for every change.

[Unreleased]: https://github.com/pixelpolishers/kiln-cli/compare/v0.1.0-rc1...HEAD
[0.1.0-rc1]: https://github.com/pixelpolishers/kiln-cli/releases/tag/v0.1.0-rc1
