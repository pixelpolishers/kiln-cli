# Kiln installer for Windows (PowerShell 5.1 or newer).
#
#   irm https://github.com/pixelpolishers/kiln-cli/releases/latest/download/install.ps1 | iex
#
# It finds out which processor this is, downloads the matching kiln archive from the
# GitHub release, checks its SHA-256 checksum, installs kiln.exe and adds its folder
# to your user PATH. Nothing else on the machine is changed.
#
# Settings (environment variables):
#   KILN_VERSION      the version to install, e.g. v0.1.0 (default: the newest stable
#                     release, or the newest release candidate while there is no stable one)
#   KILN_INSTALL_DIR  where to put kiln.exe (default: %LOCALAPPDATA%\Programs\kiln)
#
# The copy of this script that is attached to a release has the checksums of that
# release baked in (between the BAKED markers below); the copy in the repository has
# none and takes them from the release's SHA256SUMS file instead.

# Everything happens inside a function that is called on the last line, so a
# download that is cut off halfway does not run a half-read script.
function Install-Kiln {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'   # the progress bar makes downloads very slow in Windows PowerShell
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = 'pixelpolishers/kiln-cli'

    # BAKED-BEGIN (filled in by the release workflow)
    $bakedVersion = ''
    $bakedSums = ''
    # BAKED-END

    function Fail($message) { throw "kiln install: $message" }

    # ---- which processor is this? ----
    switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
        'X64'   { $arch = 'amd64' }
        'Arm64' { $arch = 'arm64' }
        default { Fail "unsupported processor: $_" }
    }
    $asset = "kiln_windows_$arch.zip"

    # ---- which version? ----
    $version = $env:KILN_VERSION
    if (-not $version) { $version = $bakedVersion }
    if (-not $version) {
        # the newest stable release; the "latest" release of GitHub ignores pre-releases,
        # so fall back to the newest release of any kind while there is no stable one
        try { $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name } catch { $version = '' }
        if (-not $version) {
            try { $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=1")[0].tag_name } catch { $version = '' }
            if (-not $version) { Fail "cannot find a release of $repo (no network, or none published yet)" }
            Write-Host "There is no stable release yet; installing the newest pre-release, $version."
        }
    }
    if (-not $version.StartsWith('v')) { $version = "v$version" }
    $base = "https://github.com/$repo/releases/download/$version"

    # ---- download ----
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("kiln-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading Kiln $version for windows/$arch ..."
        try { Invoke-WebRequest "$base/$asset" -OutFile (Join-Path $tmp $asset) -UseBasicParsing }
        catch { Fail "cannot download $base/$asset (is there a $version release with a windows/$arch binary?)" }

        # ---- check the checksum ----
        $expected = ''
        $origin = ''
        if ($bakedSums -and $version -eq $bakedVersion) {
            foreach ($line in ($bakedSums -split "`n")) {
                $parts = $line.Trim() -split '\s+'
                if ($parts.Length -ge 2 -and $parts[1] -eq $asset) { $expected = $parts[0]; $origin = 'this script' }
            }
        }
        if (-not $expected) {
            try { $sums = (Invoke-WebRequest "$base/SHA256SUMS" -UseBasicParsing).Content } catch { Fail "cannot download $base/SHA256SUMS" }
            if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
            foreach ($line in ($sums -split "`n")) {
                $parts = $line.Trim() -split '\s+'
                if ($parts.Length -ge 2 -and $parts[1] -eq $asset) { $expected = $parts[0]; $origin = "the release's SHA256SUMS" }
            }
        }
        if (-not $expected) { Fail "no checksum for $asset" }
        $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
        if ($actual -ne $expected.ToLower()) {
            Fail "checksum mismatch for ${asset}: expected $expected (from $origin), got $actual. Nothing was installed."
        }
        Write-Host "Checksum OK (from $origin)."

        # ---- install ----
        Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
        $dir = $env:KILN_INSTALL_DIR
        if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'Programs\kiln' }
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        Copy-Item (Join-Path $tmp 'kiln.exe') (Join-Path $dir 'kiln.exe') -Force
        $installed = & (Join-Path $dir 'kiln.exe') version
        Write-Host "Installed $installed in $(Join-Path $dir 'kiln.exe')"

        # ---- PATH ----
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = @($userPath -split ';' | Where-Object { $_ })
        if ($entries -notcontains $dir) {
            [Environment]::SetEnvironmentVariable('Path', (($entries + $dir) -join ';'), 'User')
            Write-Host "Added $dir to your user PATH. Open a new terminal to use kiln."
        }
        Write-Host ''
        Write-Host "Next: run 'kiln settings' to see what Kiln found on this machine, and"
        Write-Host "'kiln install --check' in a project to see which tools are missing."
    }
    finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
}

Install-Kiln
