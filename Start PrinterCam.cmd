@echo off
cd /d "%~dp0"
if not exist "%~dp0PrinterCam.exe" (
 echo Extract the complete PrinterCam folder before starting it.
 pause
 exit /b 1
)
start "PrinterCam" /min "%~dp0PrinterCam.exe"
