' Launch the Bandwidth Monitor tray widget with no visible console window.
' Double-click this file or place a shortcut to it in the Startup folder.
Set WshShell = CreateObject("WScript.Shell")
WshShell.Run "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File """ & _
    Replace(WScript.ScriptFullName, WScript.ScriptName, "") & _
    "bandwidth-monitor-tray.ps1""", 0, False
