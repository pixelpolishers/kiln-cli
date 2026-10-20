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

> **Status: working prototype.** Libraries are built from their upstream git tags with short JSON manifests, verified, cached by an exact build identity and locked. There are no prebuilt binaries and no release downloads yet, so you build `kiln` yourself (below). It needs a Kiln registry to find packages (see [Registry](#registry)).

## Requirements

To run Kiln you need:

- **git** (libraries are fetched from their git repositories)
- **CMake 3.20 or newer**
- **a C++ compiler**: GCC, Clang or MSVC
- **make** or **Ninja**

Some libraries need more (OpenSSL needs Perl, SQLite needs Tcl, and so on). `kiln install --check` tells you what is missing for the libraries in your project before anything is built, with the command to install it.

To build `kiln` itself you also need **Go 1.26 or newer**.

## Install

There are no releases yet, so Kiln is built from source. The CLI shares code with two other repositories, [kiln-core](https://github.com/pixelpolishers/kiln-core) and [kiln-schema](https://github.com/pixelpolishers/kiln-schema), so clone all three next to each other and join them in a Go workspace. The three steps are the same on every system; only the preparation differs.

### Fedora, RHEL and other Red Hat family systems

```
sudo dnf install git cmake gcc-c++ make golang
go version        # must be 1.26 or newer; if not, install Go from https://go.dev/dl/
```

### Debian, Ubuntu and other Debian family systems

```
sudo apt install git cmake g++ make
```

The Go in the Debian and Ubuntu repositories is usually older than 1.26. Install a current one from <https://go.dev/dl/>:

```
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.0.linux-amd64.tar.gz   # the file you downloaded
export PATH="$PATH:/usr/local/go/bin"                      # put this in ~/.profile too
```

### macOS

```
xcode-select --install          # Apple Clang and make
brew install go cmake git
```

### Windows

In PowerShell:

```
winget install GoLang.Go Git.Git Kitware.CMake Ninja-build.Ninja
winget install BrechtSanders.WinLibs.POSIX.UCRT     # MinGW-w64 GCC
```

Open a new terminal afterwards so the new programs are on `PATH`.

Kiln is tested on Windows with MinGW-w64 GCC and Ninja. It asks CMake for Ninja (or MinGW Makefiles when Ninja is missing) so that the compiler it detected is the one that is used. Visual Studio (MSVC) should work when you run Kiln from a *Developer Command Prompt for VS*, which puts `cl` and Ninja on the `PATH`, but that combination is not tested yet. You can force a generator with the `CMAKE_GENERATOR` environment variable.

### Build `kiln`

Linux and macOS:

```
mkdir kiln && cd kiln
git clone https://github.com/pixelpolishers/kiln-cli
git clone https://github.com/pixelpolishers/kiln-core
git clone https://github.com/pixelpolishers/kiln-schema
go work init ./kiln-cli ./kiln-core ./kiln-schema
go build -o kiln ./kiln-cli/cmd/kiln
sudo mv kiln /usr/local/bin/        # or any directory on your PATH
```

Windows (PowerShell):

```
mkdir kiln; cd kiln
git clone https://github.com/pixelpolishers/kiln-cli
git clone https://github.com/pixelpolishers/kiln-core
git clone https://github.com/pixelpolishers/kiln-schema
go work init ./kiln-cli ./kiln-core ./kiln-schema
go build -o kiln.exe ./kiln-cli/cmd/kiln
```

Move `kiln.exe` to a directory on your `PATH`.

### Check that it works

```
kiln version
kiln settings        # the build settings Kiln detected: operating system, compiler, ...
```

`kiln settings` needs no registry. If the compiler it reports is not the one you want, set the `CXX` (and `CC`) environment variables before running Kiln.

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

```
cd kiln-cli
go vet ./...
go test ./...
```

The tests that compile code need git, CMake and a C++ compiler and skip themselves when those are missing. CI runs them on Ubuntu, Fedora, macOS and Windows.
