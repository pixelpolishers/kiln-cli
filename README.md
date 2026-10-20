# kiln-cli

The `kiln` command line tool. Kiln is a package manager for C++: list your dependencies in a JSON file, run one command, and everybody on the project gets exactly the same libraries, without committing them.

```json
{ "require": { "gabime/spdlog": "^1.14", "nlohmann/json": "^3.12" } }
```

```
$ kiln require gabime/spdlog nlohmann/json   # writes kiln.json, picks versions, builds, writes the CMake integration
$ git add kiln.json kiln.lock                # everybody who clones this gets the same libraries
```

In your `CMakeLists.txt`:

```cmake
include(vendor/kiln/kiln.cmake)
target_link_libraries(app PRIVATE gabime::spdlog nlohmann::nlohmann_json)
```

> **Status: working prototype.** Libraries are built from their upstream git tags with short JSON manifests, verified, cached by an exact build identity and locked; there are no prebuilt libraries yet, everything is built on your machine. It needs a Kiln registry to find packages (see [Registry](#registry)).

## Install

Kiln is one executable, `kiln`, with nothing else to install. It uses tools that are probably on your machine already (git, CMake, a C++ compiler); the steps for each system below start with those.

### Quick install

Linux, macOS and FreeBSD:

```
curl -fsSL https://raw.githubusercontent.com/pixelpolishers/kiln-cli/master/scripts/install.sh | sh
```

or with `wget`:

```
wget -qO- https://raw.githubusercontent.com/pixelpolishers/kiln-cli/master/scripts/install.sh | sh
```

Windows (PowerShell):

```
irm https://raw.githubusercontent.com/pixelpolishers/kiln-cli/master/scripts/install.ps1 | iex
```

The script finds out which system and processor you have, downloads the matching archive of the newest release, checks its SHA-256 checksum, and installs `kiln`: into `~/.local/bin` (or `/usr/local/bin` when run as root) on Linux, macOS and FreeBSD, and into `%LOCALAPPDATA%\Programs\kiln` on Windows, which it adds to your user `PATH`. It changes nothing else. Set `KILN_VERSION` (for example `v0.1.0`) to install another version and `KILN_INSTALL_DIR` to choose the folder. It is short; read it before you run it: [install.sh](scripts/install.sh), [install.ps1](scripts/install.ps1).

Each release also carries its own copy of the scripts with the checksums of that release written into them, which can only install that release:

```
curl -fsSL https://github.com/pixelpolishers/kiln-cli/releases/download/v0.1.0/install.sh | sh
```

(Available from the first release after `v0.1.0-rc1`.) The commands above use the copy in the repository, which takes the checksums from the release itself: that catches a damaged download, but not a release that was tampered with; see [Verify the download](#verify-the-download) for stronger checks.

The rest of this section explains the same thing step by step, system by system.

> **Only a release candidate exists so far** ([v0.1.0-rc1](https://github.com/pixelpolishers/kiln-cli/releases/tag/v0.1.0-rc1)). The `releases/latest/download/...` links below ignore pre-releases and start working with the first stable release. Until then replace `latest/download` by `download/v0.1.0-rc1` in the commands, or use the quick install above.

### What Kiln needs on your machine

- **git** (libraries are fetched from their git repositories)
- **CMake 3.20 or newer**
- **a C++ compiler** (GCC, Clang or MSVC) and **make** or **Ninja**

Some libraries need more (OpenSSL needs Perl, SQLite needs Tcl, and so on). `kiln install --check` tells you what is missing for the libraries in your project before anything is built, with the command to install it.

### Linux

Fedora, RHEL and other Red Hat family systems:

```
sudo dnf install git cmake gcc-c++ make
```

Debian, Ubuntu and other Debian family systems:

```
sudo apt install git cmake g++ make
```

Then download Kiln (use `arm64` instead of `amd64` on ARM machines):

```
curl -fsSLO https://github.com/pixelpolishers/kiln-cli/releases/latest/download/kiln_linux_amd64.tar.gz
tar -xzf kiln_linux_amd64.tar.gz
sudo install -m 755 kiln /usr/local/bin/kiln
```

### macOS

```
xcode-select --install          # Apple Clang and make
brew install cmake git
```

Then download Kiln (`arm64` for Apple silicon, `amd64` for Intel Macs):

```
curl -fsSLO https://github.com/pixelpolishers/kiln-cli/releases/latest/download/kiln_darwin_arm64.tar.gz
tar -xzf kiln_darwin_arm64.tar.gz
sudo install -m 755 kiln /usr/local/bin/kiln
```

The binary is not signed or notarized yet. A file downloaded with `curl` runs without a question; one downloaded in a browser is quarantined by macOS, which you lift with `xattr -d com.apple.quarantine kiln`.

### Windows

In PowerShell, install the tools:

```
winget install Git.Git Kitware.CMake Ninja-build.Ninja
winget install BrechtSanders.WinLibs.POSIX.UCRT     # MinGW-w64 GCC
```

Then download Kiln, put it in a folder and add the folder to your `PATH`:

```
$dir = "$env:LOCALAPPDATA\Programs\kiln"
Invoke-WebRequest https://github.com/pixelpolishers/kiln-cli/releases/latest/download/kiln_windows_amd64.zip -OutFile kiln.zip
Expand-Archive kiln.zip -DestinationPath $dir -Force
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$dir", "User")
```

Open a new terminal afterwards so the new `PATH` and programs are picked up. The executable is not signed, so Windows SmartScreen may warn when you run a copy downloaded in a browser.

Kiln is tested on Windows with MinGW-w64 GCC and Ninja. It asks CMake for Ninja (or MinGW Makefiles when Ninja is missing) so that the compiler it detected is the one that is used. Visual Studio (MSVC) should work when you run Kiln from a *Developer Command Prompt for VS*, which puts `cl` and Ninja on the `PATH`, but that combination is not tested yet. You can force a generator with the `CMAKE_GENERATOR` environment variable.

### FreeBSD

```
sudo pkg install git cmake gmake
```

The C++ compiler (Clang) is part of FreeBSD. Then use the quick install above, or download `kiln_freebsd_amd64.tar.gz` from a release (the first release after `v0.1.0-rc1` has it). The FreeBSD binary is built but has never been run: please report what you find.

### Verify the download

Every release lists a `SHA256SUMS` file. To verify an archive (Linux; on macOS use `shasum -a 256 -c`):

```
curl -fsSLO https://github.com/pixelpolishers/kiln-cli/releases/latest/download/SHA256SUMS
grep kiln_linux_amd64.tar.gz SHA256SUMS | sha256sum -c
```

That shows the file is complete and equal to what the release lists. To check that the file was built from this repository by its release workflow, and not uploaded by somebody else, verify its build attestation with the [GitHub CLI](https://cli.github.com/) (releases after `v0.1.0-rc1` have one):

```
gh attestation verify kiln_linux_amd64.tar.gz --repo pixelpolishers/kiln-cli
```

### Check that it works

```
kiln version
kiln settings        # the build settings Kiln detected: operating system, compiler, ...
```

`kiln settings` needs no registry. If the compiler it reports is not the one you want, set the `CXX` (and `CC`) environment variables before running Kiln.

### Build from source

You need **Go 1.26 or newer** (Fedora: `sudo dnf install golang`; macOS: `brew install go`; Windows: `winget install GoLang.Go`; Debian and Ubuntu ship an older Go, so install one from <https://go.dev/dl/>). The CLI shares code with two other repositories, [kiln-core](https://github.com/pixelpolishers/kiln-core) and [kiln-schema](https://github.com/pixelpolishers/kiln-schema), so clone all three next to each other and join them in a Go workspace:

```
mkdir kiln && cd kiln
git clone https://github.com/pixelpolishers/kiln-cli
git clone https://github.com/pixelpolishers/kiln-core
git clone https://github.com/pixelpolishers/kiln-schema
go work init ./kiln-cli ./kiln-core ./kiln-schema
go build -o kiln ./kiln-cli/cmd/kiln
```

On Windows use `-o kiln.exe`. Move the result to a directory on your `PATH`.

## Registry

`kiln search`, `kiln info` and `kiln require` ask a registry which packages and versions exist. The registry is the [kiln-website](https://github.com/pixelpolishers/kiln-website) service. Point Kiln at one with:

```
export KILN_REGISTRY=http://localhost:8080        # the default
```

On Windows PowerShell: `$env:KILN_REGISTRY = "http://localhost:8080"`.

## Commands

| Command | Does |
|---|---|
| `search`, `info`, `resolve` | find packages and show which versions would be installed |
| `init`, `require`, `remove` | create and edit `kiln.json` |
| `install` | install what `kiln.json` asks for, exactly as `kiln.lock` says |
| `update` | resolve again and rewrite the lock |
| `outdated`, `why`, `why-not` | explain the state of the dependencies |
| `audit`, `licenses` | check the locked packages for known vulnerabilities and for a license policy |
| `cache ls`, `cache clear` | list or delete the shared cache of builds and sources |
| `settings` | show the detected build settings |
| `package`, `publish`, `finalize` | build a local binary repository |

`kiln <command> -h` lists the options of a command.

## Configuration

| Variable | Meaning |
|---|---|
| `KILN_REGISTRY` | registry URL (default `http://localhost:8080`) |
| `KILN_CACHE_DIR` | where builds and sources are cached (default: the user cache directory) |
| `KILN_LOCAL_REPO` | a local repository of prebuilt packages |
| `KILN_NO_SOURCES` | set to `1` to not copy library sources into `vendor/` (same as `--no-sources`) |
| `CXX`, `CC` | the compiler to use instead of the detected one |
| `CMAKE_GENERATOR` | the CMake generator to use instead of Kiln's choice (Windows) |

## Develop

Build `bin/kiln` from the `kiln-cli` directory (this needs `kiln-core` and `kiln-schema` next to it and a `go.work` file in the parent directory, as in [Build from source](#build-from-source)):

```
cd kiln-cli
go build -o bin/kiln ./cmd/kiln      # bin/kiln.exe on Windows
./bin/kiln version
```

Run the checks:

```
go vet ./...
go test ./...
```

The tests that compile code need git, CMake and a C++ compiler and skip themselves when those are missing. CI runs them on Ubuntu, Fedora, macOS and Windows.

## License

MIT, see [LICENSE](LICENSE).
