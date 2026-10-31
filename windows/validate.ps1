param(
    [ValidateSet('info','points','perf','latency')][string]$Mode='info',
    [string]$AgentPath='.\screen-agent-windows-amd64.exe',
    [int]$ScreenWidth=0,[int]$ScreenHeight=0,
    [int]$AgentProcessId=0,[int]$DurationSeconds=3600,
    [string]$OutputPath='',[string]$SamplesPath=''
)
$ErrorActionPreference='Stop'

if($Mode -eq 'info') {
    Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version,BuildNumber,OSArchitecture
    Get-CimInstance Win32_ComputerSystem | Select-Object SystemType,NumberOfLogicalProcessors
    & $AgentPath -check-permissions
    if(Get-Command go -ErrorAction SilentlyContinue) {go version}
    Write-Host 'MF默认路径无需FFmpeg；系统组件可用性以Agent启动和实际采集日志为准。'
    exit
}

if($Mode -eq 'latency') {
    if(!$SamplesPath) {throw 'Provide actual camera measurements with -SamplesPath'}
    $rows=@(Import-Csv $SamplesPath)
    if($rows.Count -lt 100) {throw 'At least 100 real samples are required'}
    $delays=@(foreach($row in $rows) {
        $source=[double]$row.SourceFrame
        $remote=[double]$row.RemoteFrame
        $fps=[double]$row.CaptureFPS
        if([double]::IsNaN($fps) -or [double]::IsInfinity($fps) -or [double]::IsNaN($source) -or [double]::IsInfinity($source) -or [double]::IsNaN($remote) -or [double]::IsInfinity($remote) -or $fps -lt 120 -or $source -lt 0 -or $remote -lt $source) {throw 'Invalid camera sample'}
        ($remote-$source)*1000/$fps
    })
    $sorted=@($delays | Sort-Object)
    $p95=$sorted[[int][Math]::Ceiling(0.95*$sorted.Count)-1]
    [pscustomobject]@{Samples=$sorted.Count;MeanMS=($delays | Measure-Object -Average).Average;P95MS=$p95;MaximumMS=$sorted[-1];Meets300MS=($p95 -le 300)}
    exit
}

if(!$OutputPath) {$OutputPath='windows-'+$Mode+'-'+(Get-Date -Format yyyyMMdd-HHmmss)+'.csv'}
if(Test-Path $OutputPath) {throw 'Output already exists; select another -OutputPath'}

if($Mode -eq 'points') {
    if($ScreenWidth -lt 2 -or $ScreenHeight -lt 2) {throw 'Use actual physical primary-screen dimensions with -ScreenWidth and -ScreenHeight'}
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class PhysicalCursor {
    [StructLayout(LayoutKind.Sequential)]
    public struct Point { public int X; public int Y; }
    // GetPhysicalCursorPos 获取物理坐标，与 PowerShell 自身的 DPI awareness 无关，不注入任何事件。
    [DllImport("user32.dll", SetLastError=true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static extern bool GetPhysicalCursorPos(out Point point);
}
'@
    $records=@()
    for($index=0;$index -lt 9;$index++) {
        $x=@(0,[int][Math]::Floor($ScreenWidth/2),($ScreenWidth-1))[$index%3]
        $y=@(0,[int][Math]::Floor($ScreenHeight/2),($ScreenHeight-1))[[int][Math]::Floor($index/3)]
        Write-Host "Run ninePoint($index) in the controller browser. Expected physical position: ($x,$y). Do not move the local mouse."
        [void](Read-Host 'After the remote cursor stops, press Enter on THIS Windows keyboard')
        $point=New-Object PhysicalCursor+Point
        if(![PhysicalCursor]::GetPhysicalCursorPos([ref]$point)) {throw "GetPhysicalCursorPos failed: $([Runtime.InteropServices.Marshal]::GetLastWin32Error())"}
        $errorPX=[Math]::Max([Math]::Abs($point.X-$x),[Math]::Abs($point.Y-$y))
        $record=[pscustomobject]@{TimeUTC=[DateTime]::UtcNow.ToString('o');Index=$index;ExpectedX=$x;ExpectedY=$y;ActualX=$point.X;ActualY=$point.Y;MaximumAxisErrorPX=$errorPX}
        $records+=$record
        $record | Export-Csv -NoTypeInformation -Encoding UTF8 -Append -Path $OutputPath
        $record | Format-Table
    }
    $maximum=($records | Measure-Object MaximumAxisErrorPX -Maximum).Maximum
    [pscustomobject]@{Points=$records.Count;MaximumErrorPX=$maximum;Meets5PX=($maximum -le 5);CSV=$OutputPath}
    exit
}

if($AgentProcessId -le 0 -or $DurationSeconds -lt 1 -or $DurationSeconds -gt 3600) {throw 'perf requires -AgentProcessId and 1-3600 -DurationSeconds'}
$logical=[Environment]::ProcessorCount
$clock=[Diagnostics.Stopwatch]::StartNew()
$previous=@{}
$sample=0
while($clock.Elapsed.TotalSeconds -lt $DurationSeconds) {
    $agent=Get-Process -Id $AgentProcessId -ErrorAction SilentlyContinue
    if(!$agent) {throw "Agent exited before the requested duration. Evidence: $OutputPath"}
    $processes=@($agent)
    $children=Get-CimInstance Win32_Process -Filter "ParentProcessId = $AgentProcessId AND (Name = 'screen_capture.exe' OR Name = 'ffmpeg.exe')"
    foreach($child in $children) {
        $process=Get-Process -Id $child.ProcessId -ErrorAction SilentlyContinue
        if($process) {$processes+=$process}
    }
    $current=@{}
    foreach($process in $processes) {
        try {
            $id=[int]$process.Id
            $elapsed=$clock.Elapsed.TotalSeconds
            $cpu=$process.TotalProcessorTime.TotalSeconds
            $percent=$null
            if($previous.ContainsKey($id) -and $elapsed -gt $previous[$id].Time) {
                $percent=100*($cpu-$previous[$id].CPU)/($elapsed-$previous[$id].Time)/$logical
            }
            [pscustomobject]@{TimeUTC=[DateTime]::UtcNow.ToString('o');ElapsedSeconds=$elapsed;PID=$id;Name=$process.ProcessName;CPUPercentOfMachine=$percent;WorkingSetBytes=$process.WorkingSet64;PrivateBytes=$process.PrivateMemorySize64} |
                Export-Csv -NoTypeInformation -Encoding UTF8 -Append -Path $OutputPath
            $current[$id]=@{Time=$elapsed;CPU=$cpu}
        } catch {
            if($process.Id -eq $AgentProcessId) {throw}
            Write-Warning 'Capture child exited during sampling; correlate with Agent recovery logs.'
        }
    }
    $previous=$current
    $sample++
    Start-Sleep -Milliseconds 1000
}
[pscustomobject]@{ElapsedSeconds=$clock.Elapsed.TotalSeconds;Samples=$sample;CSV=$OutputPath;Note='Process metrics only; inspect browser/video logs separately.'}
