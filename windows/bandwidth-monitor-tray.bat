@echo off
:: Launch the Bandwidth Monitor system-tray widget (debug mode).
:: This keeps a console window open so you can see errors.
:: For silent (no window) launch, use bandwidth-monitor-tray.vbs instead.

title Bandwidth Monitor Tray
chcp 65001 >nul 2>&1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0bandwidth-monitor-tray.ps1" %*
if %ERRORLEVEL% neq 0 (
    echo.
    echo Exited with error code %ERRORLEVEL%
)
echo.
pause
