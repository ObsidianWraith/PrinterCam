$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut((Join-Path ([Environment]::GetFolderPath('Desktop')) 'PrinterCam.lnk'))
$shortcut.TargetPath = Join-Path $root 'PrinterCam.exe'
$shortcut.WorkingDirectory = $root
$shortcut.WindowStyle = 7
$shortcut.Description = 'Open your local four-camera printer dashboard'
$shortcut.Save()
Write-Host 'PrinterCam shortcut created on your desktop.'
