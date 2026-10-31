# 一次执行Windows原生编译、恢复/协议测试和Go打包；仅开发机需要Go、VS C++ Build Tools及Windows SDK。
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
    if (!(Get-Command go -ErrorAction SilentlyContinue)) { throw '需要构建机安装 Go 1.25 或更新版本；目标电脑无需 Go。' }
    $vswhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
    if (!(Test-Path $vswhere)) { throw '需要 VS 2022 C++ Build Tools（含Windows SDK），或直接使用 GitHub Actions 一键构建。' }
    $vs = & $vswhere -latest -version '[17.0,18.0)' -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
    if ($LASTEXITCODE -ne 0 -or !$vs) { throw '未找到 VS 2022 x64 C++ 工具链；无需完整 Visual Studio IDE。' }
    $cmake = Join-Path $vs 'Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin\cmake.exe'
    if (!(Test-Path $cmake)) {
        $found = Get-Command cmake -ErrorAction SilentlyContinue
        if (!$found) { throw '需要 CMake 3.20+（VS安装器的C++ CMake工具组件）。' }
        $cmake = $found.Source
    }
    & $cmake -S windows/native-probe -B .cache/windows-build-r6 -G 'Visual Studio 17 2022' -A x64
    if ($LASTEXITCODE -ne 0) { throw 'CMake configure failed' }
    & $cmake --build .cache/windows-build-r6 --config Release --parallel 2
    if ($LASTEXITCODE -ne 0) { throw 'Native C++ compilation failed' }
    $ctest = Join-Path (Split-Path $cmake) 'ctest.exe'
    & $ctest --test-dir .cache/windows-build-r6 -C Release --output-on-failure
    if ($LASTEXITCODE -ne 0) { throw 'Native bitstream tests failed' }
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
    go test -count=1 -run '^(TestNative|TestAgent)' .
    if ($LASTEXITCODE -ne 0) { throw 'Agent recovery/protocol/process tests failed' }
    $agent = 'release/windows-agent-mf-x64'
    $relay = 'release/linux-relay-amd64'
    New-Item -ItemType Directory -Force -Path $agent,$relay | Out-Null
    go build -o "$agent/screen-agent-windows-amd64.exe" .
    if ($LASTEXITCODE -ne 0) { throw 'Windows Agent build failed' }
    Copy-Item .cache/windows-build-r6/Release/screen_native_probe.exe "$agent/screen_capture.exe" -Force
    Copy-Item windows/MF_AGENT.md "$agent/README.md" -Force
    Copy-Item windows/STABILITY.md "$agent/STABILITY.md" -Force
    Copy-Item windows/validate.ps1 "$agent/validate.ps1" -Force
    $env:GOOS = 'linux'
    go build -o "$relay/screen-relay" .
    if ($LASTEXITCODE -ne 0) { throw 'Linux Relay build failed' }
    Copy-Item windows/STABILITY.md "$relay/README.md" -Force
    $dumpbin = Get-ChildItem "$vs\VC\Tools\MSVC\*\bin\Hostx64\x64\dumpbin.exe" | Sort-Object FullName -Descending | Select-Object -First 1
    if (!$dumpbin) { throw 'dumpbin not found' }
    & $dumpbin.FullName /dependents "$agent/screen_capture.exe" | Out-File "$agent/capture-dependencies.txt"
    if ($LASTEXITCODE -ne 0) { throw 'Native dependency inspection failed' }
    Get-FileHash "$agent/*.exe" -Algorithm SHA256 | Format-List | Out-File "$agent/SHA256.txt"
    'revision=6. Windows compilation, bitstream and Agent recovery/protocol tests passed. Real DXGI recovery, public streaming and 30-minute stability are NOT verified by building.' | Out-File "$agent/BUILD-STATUS.txt"
    Compress-Archive -Path "$agent/*" -DestinationPath release/windows-agent-mf-x64.zip -Force
    Compress-Archive -Path "$relay/*" -DestinationPath release/linux-relay-amd64.zip -Force
    Write-Host '生成 release/windows-agent-mf-x64.zip 和 release/linux-relay-amd64.zip；构建通过不代表Windows运行验收通过。'
} finally {
    $env:GOOS = $oldGOOS; $env:GOARCH = $oldGOARCH; $env:CGO_ENABLED = $oldCGO
    Pop-Location
}
