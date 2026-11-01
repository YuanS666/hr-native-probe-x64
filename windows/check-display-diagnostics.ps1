# 只解析脚本并编译内嵌C#，不调用Run，不创建窗口或执行任何显示电源操作；用于Windows CI。
$ErrorActionPreference='Stop'
foreach ($name in @('test-display-wake.ps1','observe-display.ps1')) {
    $path=Join-Path $PSScriptRoot $name
    $tokens=$null; $parseErrors=$null
    [System.Management.Automation.Language.Parser]::ParseFile($path,[ref]$tokens,[ref]$parseErrors) | Out-Null
    if($parseErrors.Count) { throw ($parseErrors | Out-String) }
    $source=Get-Content -Raw -LiteralPath $path
    $match=[regex]::Match($source,"(?s)@'\r?\n(.*?)\r?\n'@")
    if(!$match.Success) {throw "Missing C# source: $name"}
    if($name -eq 'observe-display.ps1') {
        Add-Type -ReferencedAssemblies @('System.dll','System.Windows.Forms.dll') -TypeDefinition $match.Groups[1].Value
    } else {
        Add-Type -TypeDefinition $match.Groups[1].Value
    }
    Write-Host "PASS: parse and C# compile $name (no power calls)"
}
