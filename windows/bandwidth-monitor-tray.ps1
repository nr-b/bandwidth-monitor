<#
.SYNOPSIS
    Bandwidth Monitor - Windows system-tray widget.

.DESCRIPTION
    Creates a system-tray (notification area) icon that polls the
    bandwidth-monitor /api/summary endpoint every 5 seconds.
    Current WAN download/upload rates are shown in the icon tooltip,
    and right-clicking opens a context menu with full details
    (interfaces, DNS, WiFi, NAT) plus a link to the web dashboard.

    This is the Windows counterpart of swiftbar/bandwidth-monitor.5s.sh.

.PARAMETER Server
    Base URL of the bandwidth-monitor server.
    Default: auto-detect from the default gateway on port 8080.

.PARAMETER Port
    Port to use when auto-detecting the server from the default gateway.
    Default: 8080

.PARAMETER PreferIface
    Preferred interface name to show in the tooltip.

.PARAMETER RefreshSeconds
    Polling interval in seconds.  Default: 5

.PARAMETER ShowExternalIP
    Query ip.ffmuc.net for public IPv4/IPv6 addresses (cached ~5 min).
    Set to "false" to disable. Default: true
    NOTE: This contacts an external service (https://ip.ffmuc.net).

.EXAMPLE
    .\bandwidth-monitor-tray.ps1
    .\bandwidth-monitor-tray.ps1 -Server http://192.0.2.1:8080
    .\bandwidth-monitor-tray.ps1 -Server http://198.51.100.1:8080 -PreferIface eth0
    .\bandwidth-monitor-tray.ps1 -ShowExternalIP false
#>

[CmdletBinding()]
param(
    [string]$Server = $env:BW_SERVER,
    [int]$Port = $(if ($env:BW_PORT) { [int]$env:BW_PORT } else { 8080 }),
    [string]$PreferIface = $env:BW_PREFER_IFACE,
    [int]$RefreshSeconds = 5,
    [string]$ShowExternalIP = $(if ($env:BW_SHOW_EXTERNAL_IP) { $env:BW_SHOW_EXTERNAL_IP } else { "true" })
)

# -- Dependencies ---------------------------------------------------------
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

# -- Auto-detect server from default gateway ------------------------------
if (-not $Server) {
    try {
        $gw = (Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue |
               Sort-Object RouteMetric | Select-Object -First 1).NextHop
        if ($gw) {
            $Server = "http://${gw}:${Port}"
        }
    } catch { }
    if (-not $Server) { $Server = "http://localhost:${Port}" }
}
Write-Host "bandwidth-monitor tray: polling $Server every ${RefreshSeconds}s"
if ($ShowExternalIP -eq "true") {
    Write-Host "External IP lookup enabled (ip.ffmuc.net, cached ~5 min)"
} else {
    Write-Host "External IP lookup disabled"
}

# -- Helpers --------------------------------------------------------------
function Format-Rate([double]$bytesPerSec) {
    $mbps = $bytesPerSec * 8 / 1e6
    if ([Math]::Abs($mbps) -ge 1) {
        return "{0:N1} Mb/s" -f [Math]::Abs($mbps)
    }
    $kbps = [Math]::Abs($bytesPerSec) * 8 / 1e3
    return "{0:N0} Kb/s" -f $kbps
}

# Compact rate for icon overlay (max ~4 chars): "12M", "800K", "1.2G"
function Format-RateShort([double]$bytesPerSec) {
    $mbps = [Math]::Abs($bytesPerSec) * 8 / 1e6
    if ($mbps -ge 1000) {
        return "{0:N0}G" -f ($mbps / 1000)
    } elseif ($mbps -ge 100) {
        return "{0:N0}M" -f $mbps
    } elseif ($mbps -ge 1) {
        return "{0:N0}M" -f $mbps
    } else {
        $kbps = [Math]::Abs($bytesPerSec) * 8 / 1e3
        if ($kbps -ge 1) {
            return "{0:N0}K" -f $kbps
        }
        return "0"
    }
}

function Get-Summary {
    try {
        $response = Invoke-RestMethod -Uri "$Server/api/summary" `
            -TimeoutSec 2 -UserAgent "bandwidth-monitor-tray/1.0" `
            -ErrorAction Stop
        return $response
    } catch {
        return $null
    }
}

# -- External IP lookup (cached) ------------------------------------------
$script:extIP4 = ""
$script:extIP6 = ""
$script:extIPLastCheck = [datetime]::MinValue
$extIPCacheSec = 300  # refresh every ~5 minutes

function Update-ExternalIPs {
    if ($ShowExternalIP -ne "true") { return }
    $now = [datetime]::UtcNow
    if (($now - $script:extIPLastCheck).TotalSeconds -lt $extIPCacheSec) { return }
    $script:extIPLastCheck = $now

    # Use dedicated v4/v6 endpoints to get each address family reliably.
    $script:extIP4 = ""
    try {
        $script:extIP4 = (Invoke-RestMethod -Uri "https://anycast-v4.ffmuc.net/" `
            -TimeoutSec 2 -UserAgent "bandwidth-monitor-tray/1.0" `
            -ErrorAction Stop).Trim()
    } catch { $script:extIP4 = "" }

    $script:extIP6 = ""
    try {
        $script:extIP6 = (Invoke-RestMethod -Uri "https://anycast-v6.ffmuc.net/" `
            -TimeoutSec 2 -UserAgent "bandwidth-monitor-tray/1.0" `
            -ErrorAction Stop).Trim()
    } catch { $script:extIP6 = "" }
}

# -- Build Tray Icon ------------------------------------------------------
$appContext = New-Object System.Windows.Forms.ApplicationContext

$trayIcon = New-Object System.Windows.Forms.NotifyIcon
$trayIcon.Text = "Bandwidth Monitor - connecting..."
$trayIcon.Visible = $true

# Use the system's DPI-aware small icon size (typically 16x16 at 100%,
# 20x20 at 125%, 24x24 at 150%, 32x32 at 200%).
$script:iconSize = [System.Windows.Forms.SystemInformation]::SmallIconSize

# Draw a tray icon with live rates rendered as two lines:
#   top line:    down arrow + compact rate
#   bottom line: up arrow + compact rate
# Falls back to a centered label (e.g. "BW", "--") when no rates given.
# Background is always dark for visual stability.
function New-TrayIcon {
    param(
        [string]$label = "BW",
        [string]$downRate = "",
        [string]$upRate = "",
        [bool]$isError = $false
    )
    $sz = $script:iconSize
    $w = $sz.Width
    $h = $sz.Height
    $bmp = New-Object System.Drawing.Bitmap($w, $h)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = 'AntiAlias'
    $g.TextRenderingHint = 'AntiAliasGridFit'

    # Stable dark background (red only when server unreachable)
    if ($isError) {
        $g.Clear([System.Drawing.Color]::FromArgb(180, 40, 40))
    } else {
        $g.Clear([System.Drawing.Color]::FromArgb(34, 34, 34))
    }

    if ($downRate -and $upRate) {
        # Scale font size relative to icon size
        $fontSize = [Math]::Max(5, [Math]::Floor($h * 0.35))
        $fontRate = New-Object System.Drawing.Font("Segoe UI", $fontSize)
        $sf = New-Object System.Drawing.StringFormat
        $sf.Alignment = 'Near'
        $sf.LineAlignment = 'Near'
        $sf.FormatFlags = 'NoWrap'

        $halfH = [Math]::Floor($h / 2)
        $arrowX = 0
        $textX = [Math]::Floor($w * 0.3)

        # Down arrow (green) + rate
        $greenBrush = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(100, 220, 100))
        $g.DrawString([string][char]0x2193, $fontRate, $greenBrush, $arrowX, -1, $sf)
        $whiteBrush = [System.Drawing.Brushes]::White
        $g.DrawString($downRate, $fontRate, $whiteBrush, $textX, -1, $sf)

        # Up arrow (orange) + rate
        $orangeBrush = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 180, 60))
        $g.DrawString([string][char]0x2191, $fontRate, $orangeBrush, $arrowX, ($halfH - 1), $sf)
        $g.DrawString($upRate, $fontRate, $whiteBrush, $textX, ($halfH - 1), $sf)

        $greenBrush.Dispose()
        $orangeBrush.Dispose()
        $fontRate.Dispose()
        $sf.Dispose()
    } else {
        # Fallback: centered label
        $fontSize = [Math]::Max(6, [Math]::Floor($h * 0.4))
        $font = New-Object System.Drawing.Font("Segoe UI", $fontSize, [System.Drawing.FontStyle]::Bold)
        $brush = [System.Drawing.Brushes]::White
        $sf = New-Object System.Drawing.StringFormat
        $sf.Alignment = 'Center'
        $sf.LineAlignment = 'Center'
        $rect = New-Object System.Drawing.RectangleF(0, 0, $w, $h)
        $g.DrawString($label, $font, $brush, $rect, $sf)
        $font.Dispose()
        $sf.Dispose()
    }

    $g.Dispose()
    return $bmp.GetHicon()
}

# Default icon
$trayIcon.Icon = [System.Drawing.Icon]::FromHandle((New-TrayIcon "BW"))

# -- Context Menu ---------------------------------------------------------
$contextMenu = New-Object System.Windows.Forms.ContextMenuStrip
$contextMenu.ShowImageMargin = $false

$headerItem = $contextMenu.Items.Add("Bandwidth Monitor")
$headerItem.Enabled = $false
$headerItem.Font = New-Object System.Drawing.Font("Segoe UI", 9, [System.Drawing.FontStyle]::Bold)

$contextMenu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$wanItem = $contextMenu.Items.Add("WAN: --")
$wanItem.Enabled = $false

$extIP4Item = $contextMenu.Items.Add("")
$extIP4Item.Visible = $false
$extIP4Item.Enabled = $false
$extIP4Item.Font = New-Object System.Drawing.Font("Consolas", 8.25)

$extIP6Item = $contextMenu.Items.Add("")
$extIP6Item.Visible = $false
$extIP6Item.Enabled = $false
$extIP6Item.Font = New-Object System.Drawing.Font("Consolas", 8.25)

$contextMenu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$trafficHeader = $contextMenu.Items.Add("Traffic")
$trafficHeader.Enabled = $false
$trafficHeader.Font = New-Object System.Drawing.Font("Segoe UI", 8.25, [System.Drawing.FontStyle]::Bold)

# Placeholder items for interfaces (up to 10)
$ifaceItems = @()
for ($i = 0; $i -lt 10; $i++) {
    $item = $contextMenu.Items.Add("")
    $item.Visible = $false
    $item.Enabled = $false
    $item.Font = New-Object System.Drawing.Font("Consolas", 8.25)
    $ifaceItems += $item
}

$sepDns = New-Object System.Windows.Forms.ToolStripSeparator
$contextMenu.Items.Add($sepDns)
$sepDns.Visible = $false

$dnsHeader = $contextMenu.Items.Add("DNS")
$dnsHeader.Visible = $false
$dnsHeader.Enabled = $false
$dnsHeader.Font = New-Object System.Drawing.Font("Segoe UI", 8.25, [System.Drawing.FontStyle]::Bold)

$dnsItems = @()
for ($i = 0; $i -lt 3; $i++) {
    $item = $contextMenu.Items.Add("")
    $item.Visible = $false
    $item.Enabled = $false
    $item.Font = New-Object System.Drawing.Font("Consolas", 8.25)
    $dnsItems += $item
}

$sepWifi = New-Object System.Windows.Forms.ToolStripSeparator
$contextMenu.Items.Add($sepWifi)
$sepWifi.Visible = $false

$wifiHeader = $contextMenu.Items.Add("WiFi")
$wifiHeader.Visible = $false
$wifiHeader.Enabled = $false
$wifiHeader.Font = New-Object System.Drawing.Font("Segoe UI", 8.25, [System.Drawing.FontStyle]::Bold)

$wifiItems = @()
for ($i = 0; $i -lt 2; $i++) {
    $item = $contextMenu.Items.Add("")
    $item.Visible = $false
    $item.Enabled = $false
    $item.Font = New-Object System.Drawing.Font("Consolas", 8.25)
    $wifiItems += $item
}

$sepNat = New-Object System.Windows.Forms.ToolStripSeparator
$contextMenu.Items.Add($sepNat)
$sepNat.Visible = $false

$natHeader = $contextMenu.Items.Add("NAT - Conntrack")
$natHeader.Visible = $false
$natHeader.Enabled = $false
$natHeader.Font = New-Object System.Drawing.Font("Segoe UI", 8.25, [System.Drawing.FontStyle]::Bold)

$natItems = @()
for ($i = 0; $i -lt 3; $i++) {
    $item = $contextMenu.Items.Add("")
    $item.Visible = $false
    $item.Enabled = $false
    $item.Font = New-Object System.Drawing.Font("Consolas", 8.25)
    $natItems += $item
}

$contextMenu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$openDashboard = $contextMenu.Items.Add("Open Dashboard")
$openDashboard.add_Click({ Start-Process $Server })

$serverInfo = $contextMenu.Items.Add("Server: $Server")
$serverInfo.Enabled = $false
$serverInfo.ForeColor = [System.Drawing.Color]::Gray

$contextMenu.Items.Add((New-Object System.Windows.Forms.ToolStripSeparator))

$exitItem = $contextMenu.Items.Add("Exit")
$exitItem.add_Click({
    $trayIcon.Visible = $false
    $trayIcon.Dispose()
    [System.Windows.Forms.Application]::Exit()
})

$trayIcon.ContextMenuStrip = $contextMenu

# Double-click opens dashboard
$trayIcon.add_DoubleClick({ Start-Process $Server })

# -- Polling Timer --------------------------------------------------------
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = $RefreshSeconds * 1000

$script:lastError = $false

$tickHandler = {
    param($sender, $e)
    $data = Get-Summary

    if (-not $data) {
        $trayIcon.Text = "Bandwidth Monitor - unreachable"
        $trayIcon.Icon = [System.Drawing.Icon]::FromHandle(
            (New-TrayIcon -label "--" -isError $true))
        $wanItem.Text = "Server unreachable"
        $wanItem.ForeColor = [System.Drawing.Color]::Red
        # Hide dynamic sections
        foreach ($item in $ifaceItems) { $item.Visible = $false }
        $extIP4Item.Visible = $false; $extIP6Item.Visible = $false
        $sepDns.Visible = $false; $dnsHeader.Visible = $false
        foreach ($item in $dnsItems) { $item.Visible = $false }
        $sepWifi.Visible = $false; $wifiHeader.Visible = $false
        foreach ($item in $wifiItems) { $item.Visible = $false }
        $sepNat.Visible = $false; $natHeader.Visible = $false
        foreach ($item in $natItems) { $item.Visible = $false }
        $script:lastError = $true
        return
    }

    # Verify it's actually bandwidth-monitor
    if ($data.app -ne "bandwidth-monitor") {
        $trayIcon.Text = "Bandwidth Monitor - not a valid instance"
        $wanItem.Text = "Not a bandwidth-monitor instance"
        $wanItem.ForeColor = [System.Drawing.Color]::Red
        return
    }

    $script:lastError = $false

    # Sort interfaces: active first (state up/unknown), then down
    $active = @($data.interfaces | Where-Object { $_.state -eq 'up' -or $_.state -eq 'unknown' } |
        Sort-Object { -($_.rx_rate + $_.tx_rate) })
    $down = @($data.interfaces | Where-Object { $_.state -ne 'up' -and $_.state -ne 'unknown' })

    # Pick primary interface: preferred > WAN-tagged > highest rate
    $pri = $null
    if ($PreferIface) {
        $pri = $active | Where-Object { $_.name -eq $PreferIface } | Select-Object -First 1
    }
    if (-not $pri) {
        $pri = $active | Where-Object { $_.wan -eq $true } | Select-Object -First 1
    }
    if (-not $pri) {
        $pri = $active | Select-Object -First 1
    }

    # Tooltip (max 63 chars for NotifyIcon.Text)
    $rxFmt = Format-Rate ($pri.rx_rate)
    $txFmt = Format-Rate ($pri.tx_rate)
    $wanName = if ($pri.name) { $pri.name } else { "WAN" }
    $vpnTag = if ($data.vpn) { " [VPN]" } else { "" }
    $tip = "${wanName}${vpnTag}: down ${rxFmt} / up ${txFmt}"
    if ($tip.Length -gt 63) { $tip = $tip.Substring(0, 63) }
    $trayIcon.Text = $tip

    # Render live rates on the icon (stable dark background)
    $downShort = Format-RateShort ($pri.rx_rate)
    $upShort   = Format-RateShort ($pri.tx_rate)
    $trayIcon.Icon = [System.Drawing.Icon]::FromHandle(
        (New-TrayIcon -downRate $downShort -upRate $upShort))

    # WAN line
    $wanLabel = if ($PreferIface -and ($active | Where-Object { $_.name -eq $PreferIface })) {
        "WAN: $PreferIface (preferred)"
    } elseif ($pri.wan) {
        "WAN: $($pri.name)"
    } else {
        "WAN: $($pri.name) (highest rate)"
    }
    $wanItem.Text = $wanLabel
    $wanItem.ForeColor = [System.Drawing.Color]::Gray

    # External IPs (cached, refreshed every ~5 min)
    Update-ExternalIPs
    if ($script:extIP4) {
        $extIP4Item.Text = "  IPv4: $($script:extIP4)"
        $extIP4Item.Visible = $true
    } else {
        $extIP4Item.Visible = $false
    }
    if ($script:extIP6) {
        $extIP6Item.Text = "  IPv6: $($script:extIP6)"
        $extIP6Item.Visible = $true
    } else {
        $extIP6Item.Visible = $false
    }

    # Interface items
    $allIfaces = @($active) + @($down)
    for ($i = 0; $i -lt $ifaceItems.Count; $i++) {
        if ($i -lt $allIfaces.Count) {
            $iface = $allIfaces[$i]
            if ($iface.state -eq 'up' -or $iface.state -eq 'unknown') {
                $rx = Format-Rate ($iface.rx_rate)
                $tx = Format-Rate ($iface.tx_rate)
                $ifaceItems[$i].Text = "  $($iface.name): Down $rx  Up $tx"
                $ifaceItems[$i].ForeColor = [System.Drawing.SystemColors]::ControlText
            } else {
                $ifaceItems[$i].Text = "  $($iface.name): down"
                $ifaceItems[$i].ForeColor = [System.Drawing.Color]::Gray
            }
            $ifaceItems[$i].Visible = $true
        } else {
            $ifaceItems[$i].Visible = $false
        }
    }

    # DNS section
    if ($data.dns) {
        $sepDns.Visible = $true
        $prov = if ($data.dns.provider_name) { $data.dns.provider_name } else { "DNS" }
        $dnsHeader.Text = "DNS - $prov"
        $dnsHeader.Visible = $true
        $blockPct = [Math]::Round($data.dns.block_pct, 1)
        $latMs    = [Math]::Round($data.dns.latency_ms, 1)
        $dnsItems[0].Text = "  Queries:  $($data.dns.total_queries)"
        $dnsItems[0].Visible = $true
        $dnsItems[1].Text = "  Blocked:  $($data.dns.blocked) (${blockPct}%)"
        $dnsItems[1].Visible = $true
        $dnsItems[1].ForeColor = [System.Drawing.Color]::FromArgb(239, 68, 68)
        $dnsItems[2].Text = "  Latency:  ${latMs} ms"
        $dnsItems[2].Visible = $true
    } else {
        $sepDns.Visible = $false; $dnsHeader.Visible = $false
        foreach ($item in $dnsItems) { $item.Visible = $false }
    }

    # WiFi section
    if ($data.wifi) {
        $sepWifi.Visible = $true
        $prov = if ($data.wifi.provider_name) { $data.wifi.provider_name } else { "WiFi" }
        $wifiHeader.Text = "WiFi - $prov"
        $wifiHeader.Visible = $true
        $wifiItems[0].Text = "  APs:      $($data.wifi.aps)"
        $wifiItems[0].Visible = $true
        $wifiItems[1].Text = "  Clients:  $($data.wifi.clients)"
        $wifiItems[1].Visible = $true
    } else {
        $sepWifi.Visible = $false; $wifiHeader.Visible = $false
        foreach ($item in $wifiItems) { $item.Visible = $false }
    }

    # NAT section
    if ($data.nat) {
        $sepNat.Visible = $true
        $natHeader.Visible = $true
        $usagePct = [Math]::Round($data.nat.usage_pct, 1)
        $natItems[0].Text = "  Connections: $($data.nat.total)/$($data.nat.max) (${usagePct}%)"
        $natItems[0].Visible = $true
        if ($data.nat.usage_pct -gt 80) {
            $natItems[0].ForeColor = [System.Drawing.Color]::FromArgb(239, 68, 68)
        } elseif ($data.nat.usage_pct -gt 50) {
            $natItems[0].ForeColor = [System.Drawing.Color]::FromArgb(234, 179, 8)
        } else {
            $natItems[0].ForeColor = [System.Drawing.SystemColors]::ControlText
        }
        $natItems[1].Text = "  IPv4: $($data.nat.ipv4)  IPv6: $($data.nat.ipv6)"
        $natItems[1].Visible = $true
        $natItems[2].Text = "  SNAT: $($data.nat.snat)  DNAT: $($data.nat.dnat)"
        $natItems[2].Visible = $true
    } else {
        $sepNat.Visible = $false; $natHeader.Visible = $false
        foreach ($item in $natItems) { $item.Visible = $false }
    }
}

$timer.add_Tick($tickHandler)

# Fire immediately then start timer
& $tickHandler $null ([System.EventArgs]::Empty)
$timer.Start()

# -- Run ------------------------------------------------------------------
[System.Windows.Forms.Application]::Run($appContext)
