(function() {
    'use strict';

    // ── Theme management ──
    (function initTheme() {
        var saved = localStorage.getItem('bw-theme') || 'auto';
        document.documentElement.setAttribute('data-theme', saved);
        var toggle = document.getElementById('themeToggle');
        if (toggle) {
            var btns = toggle.querySelectorAll('.theme-btn');
            btns.forEach(function(b) {
                b.classList.toggle('active', b.getAttribute('data-theme-val') === saved);
            });
            toggle.addEventListener('click', function(e) {
                var btn = e.target.closest('.theme-btn');
                if (!btn) return;
                var val = btn.getAttribute('data-theme-val');
                document.documentElement.setAttribute('data-theme', val);
                localStorage.setItem('bw-theme', val);
                btns.forEach(function(b) { b.classList.toggle('active', b.getAttribute('data-theme-val') === val); });
                if (window._updateChartsForTheme) window._updateChartsForTheme();
            });
        }
        if (window.matchMedia) {
            window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function() {
                if ((localStorage.getItem('bw-theme') || 'auto') === 'auto' && window._updateChartsForTheme) window._updateChartsForTheme();
            });
        }
    })();

    // ── Tab navigation ──
    var _activeTab = 'traffic';
    var _lastPayload = null; // cached latest SSE data for immediate render on tab switch

    window._switchTab = function(tab) {
        _activeTab = tab;
        var panels = { traffic: 'tabTraffic', nat: 'tabNat', dns: 'tabDns', wifi: 'tabWifi', network: 'tabNetwork', monitor: 'tabMonitor', speedtest: 'tabSpeedtest', debug: 'tabDebug' };
        for (var k in panels) {
            var p = document.getElementById(panels[k]);
            if (p) p.classList.toggle('active', k === tab);
        }
        document.querySelectorAll('.main-nav-tab').forEach(function(t) {
            t.classList.toggle('active', t.getAttribute('data-tab') === tab);
        });
        // Update URL hash without scrolling
        if (history.replaceState) {
            history.replaceState(null, '', '#' + tab);
        } else {
            location.hash = tab;
        }
        if (tab === 'speedtest' && !_stHistoryLoaded) loadSpeedTestHistory();
        // Immediately render the newly active tab with cached data
        if (_lastPayload) _renderTab(tab, _lastPayload, true);
    };

    function _renderTab(tab, d, force) {
        var bw = d.top_bandwidth || [], vol = d.top_volume || [];
        var bwRemote = bw.filter(function(t) { return !t.is_local; });
        var volRemote = vol.filter(function(t) { return !t.is_local; });
        if (tab === 'traffic') {
            updateProtoChart(d.protocols);
            updateIPVersions(d.ip_versions);
            updateCountries(d.countries);
            updateASNs(d.asns);
            renderTalkers('bwTable', bwRemote, 'rate_bytes', formatRate, 'bw');
            renderTalkers('volTable', volRemote, 'total_bytes', formatBytes, 'vol');
            if (!_historyLoaded) loadInterfaceHistory();
        } else if (tab === 'monitor') {
            var now = Date.now();
            if (force || !window._lastMapUpdate || now - window._lastMapUpdate > 5000) {
                updateWorldMap(d.countries, bwRemote, d.origin_country, d.origin_lat, d.origin_lon);
                window._lastMapUpdate = now;
            }
            if (force || !window._lastLatUpdate || now - window._lastLatUpdate > 2000) {
                updateLatency(d.latency);
                window._lastLatUpdate = now;
            }
        } else if (tab === 'dns') {
            updateDNS(d.dns || null);
        } else if (tab === 'wifi') {
            updateWiFi(d.wifi || null);
        } else if (tab === 'network') {
            updateNetwork(d.topology || null, d.top_bandwidth || []);
        } else if (tab === 'nat') {
            updateNAT(d.conntrack || null);
        }
    }

    // Restore tab from URL hash on load
    (function() {
        var hash = location.hash.replace('#', '');
        var validTabs = ['traffic', 'nat', 'dns', 'wifi', 'network', 'monitor', 'speedtest', 'debug'];
        if (hash && validTabs.indexOf(hash) !== -1) {
            // Defer to ensure DOM is ready
            setTimeout(function() { window._switchTab(hash); }, 0);
        }
        // Handle browser back/forward
        window.addEventListener('hashchange', function() {
            var h = location.hash.replace('#', '');
            if (h && validTabs.indexOf(h) !== -1 && h !== _activeTab) {
                window._switchTab(h);
            }
        });
    })();

    function formatBytes(bytes, dec) {
        if (dec === undefined) dec = 1;
        if (bytes === 0) return '0 B';
        var k = 1024, s = ['B','KB','MB','GB','TB'];
        var i = Math.floor(Math.log(bytes) / Math.log(k));
        return (bytes / Math.pow(k, i)).toFixed(dec) + ' ' + s[i];
    }

    function formatRate(bps) {
        var mbps = (bps * 8) / 1e6;
        if (mbps < 0.01 && mbps > 0) return mbps.toFixed(4) + ' Mbit/s';
        if (mbps < 1) return mbps.toFixed(2) + ' Mbit/s';
        if (mbps < 100) return mbps.toFixed(1) + ' Mbit/s';
        return mbps.toFixed(0) + ' Mbit/s';
    }

    function rankClass(i) { return i === 0 ? 'rank rank-1' : 'rank'; }

    function formatPPS(pps) {
        if (pps === 0) return '0 pps';
        if (pps < 1000) return pps.toFixed(0) + ' pps';
        if (pps < 1e6) return (pps / 1000).toFixed(1) + ' Kpps';
        return (pps / 1e6).toFixed(1) + ' Mpps';
    }

    function formatUptime(secs) {
        if (!secs || secs <= 0) return '—';
        var d = Math.floor(secs / 86400);
        var h = Math.floor((secs % 86400) / 3600);
        var m = Math.floor((secs % 3600) / 60);
        if (d > 0) return d + 'd ' + h + 'h';
        if (h > 0) return h + 'h ' + m + 'm';
        return m + 'm';
    }

    // Convert ISO 3166-1 alpha-2 to flag emoji
    function countryFlag(cc) {
        if (!cc || cc.length !== 2) return '';
        var a = cc.toUpperCase();
        return String.fromCodePoint(0x1F1E6 + a.charCodeAt(0) - 65, 0x1F1E6 + a.charCodeAt(1) - 65);
    }

    var chartColors = [
        { rx: '#22d3ee', tx: '#a78bfa', rxBg: 'rgba(34,211,238,0.08)', txBg: 'rgba(167,139,250,0.08)' },
        { rx: '#34d399', tx: '#fb923c', rxBg: 'rgba(52,211,153,0.08)', txBg: 'rgba(251,146,60,0.08)' },
        { rx: '#60a5fa', tx: '#f472b6', rxBg: 'rgba(96,165,250,0.08)', txBg: 'rgba(244,114,182,0.08)' },
        { rx: '#fbbf24', tx: '#e879f9', rxBg: 'rgba(251,191,36,0.08)', txBg: 'rgba(232,121,249,0.08)' },
    ];

    var ctx = document.getElementById('trafficChart').getContext('2d');
    var zeroLinePlugin = {
        id: 'zeroLine',
        afterDraw: function(chart) {
            if (chart.canvas.id !== 'trafficChart') return;
            var yScale = chart.scales.y;
            if (!yScale) return;
            var y = yScale.getPixelForValue(0);
            if (y < yScale.top || y > yScale.bottom) return;
            var ctx = chart.ctx;
            ctx.save();
            ctx.beginPath();
            ctx.moveTo(chart.chartArea.left, y);
            ctx.lineTo(chart.chartArea.right, y);
            ctx.lineWidth = 1;
            ctx.strokeStyle = getComputedStyle(document.documentElement).getPropertyValue('--text-2').trim() || '#71717a';
            ctx.setLineDash([4, 3]);
            ctx.stroke();
            ctx.restore();
        }
    };

    var trafficChart = new Chart(ctx, {
        type: 'line',
        data: { datasets: [] },
        plugins: [zeroLinePlugin],
        options: {
            responsive: true,
            maintainAspectRatio: false,
            animation: { duration: 300 },
            interaction: { mode: 'index', intersect: false },
            plugins: {
                legend: {
                    labels: { color: '#71717a', font: { size: 11, family: 'Inter' }, boxWidth: 12, padding: 16 }
                },
                tooltip: {
                    backgroundColor: '#18181b',
                    titleColor: '#fafafa',
                    bodyColor: '#a1a1aa',
                    borderColor: '#27272a',
                    borderWidth: 1,
                    padding: 10,
                    cornerRadius: 6,
                    titleFont: { size: 12 },
                    bodyFont: { size: 12, family: 'JetBrains Mono' },
                    callbacks: {
                        label: function(c) { return c.dataset.label + ': ' + formatRate(Math.abs(c.raw.y)); }
                    }
                }
            },
            scales: {
                x: {
                    type: 'time',
                    time: { unit: 'minute', displayFormats: { minute: 'HH:mm' } },
                    min: function() { return Date.now() - 3600000; },
                    max: function() { return Date.now(); },
                    grid: { color: '#1f1f23' },
                    ticks: { color: '#52525b', font: { size: 10 }, maxTicksLimit: 12, source: 'auto' },
                    border: { color: '#27272a' }
                },
                y: {
                    grid: { color: '#1f1f23' },
                    ticks: { color: '#52525b', font: { size: 10 }, callback: function(v) { return formatRate(Math.abs(v)); } },
                    border: { color: '#27272a' },
                    grace: '15%'
                }
            }
        }
    });

    var protoColors = { 'TCP': '#3b82f6', 'UDP': '#22d3ee', 'ICMP': '#f59e0b', 'Other': '#71717a' };
    var geoChartPalette = ['#3b82f6','#22d3ee','#a78bfa','#34d399','#f59e0b','#f472b6','#60a5fa','#e879f9','#fb923c','#4ade80','#818cf8','#fbbf24','#c084fc','#2dd4bf','#f87171','#71717a'];

    // ── 24h History Chart ──
    var historyCtx = document.getElementById('historyChart').getContext('2d');
    var historyChart = new Chart(historyCtx, {
        type: 'line',
        data: { datasets: [] },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            animation: false,
            interaction: { mode: 'index', intersect: false },
            plugins: {
                legend: {
                    labels: { color: '#71717a', font: { size: 11, family: 'Inter' }, boxWidth: 12, padding: 16 }
                },
                tooltip: {
                    backgroundColor: '#18181b', titleColor: '#fafafa', bodyColor: '#a1a1aa',
                    borderColor: '#27272a', borderWidth: 1, padding: 10, cornerRadius: 6,
                    callbacks: {
                        label: function(c) { return c.dataset.label + ': ' + formatRate(Math.abs(c.raw.y)); }
                    }
                }
            },
            scales: {
                x: {
                    type: 'time',
                    time: { unit: 'hour', displayFormats: { hour: 'HH:mm' }, tooltipFormat: 'HH:mm:ss' },
                    min: function() { return Date.now() - 86400000; },
                    max: function() { return Date.now(); },
                    grid: { color: '#1f1f23' },
                    ticks: { color: '#52525b', font: { size: 10 }, maxTicksLimit: 12, source: 'auto' },
                    border: { color: '#27272a' }
                },
                y: {
                    grid: { color: '#1f1f23' },
                    ticks: { color: '#52525b', font: { size: 10 }, callback: function(v) { return formatRate(Math.abs(v)); } },
                    border: { color: '#27272a' },
                    grace: '10%'
                }
            }
        }
    });
    var _historyLoaded = false;
    var _historyRefreshInterval = null;
    function _fetchInterfaceHistory() {
        fetch('/api/interfaces/history').then(function(r) { return r.json(); }).then(function(data) {
            var ds = [], ci = 0;
            var names = Object.keys(data).sort();
            var earliestTs = Infinity;
            for (var ni = 0; ni < names.length; ni++) {
                var name = names[ni];
                var pts = data[name];
                if (!pts || !pts.length) continue;
                if (pts[0].t < earliestTs) earliestTs = pts[0].t;
                var c = chartColors[ci % chartColors.length];
                var rxData = [], txData = [];
                for (var pi = 0; pi < pts.length; pi++) {
                    var t = new Date(pts[pi].t);
                    rxData.push({ x: t, y: pts[pi].rx || 0 });
                    txData.push({ x: t, y: -(pts[pi].tx || 0) });
                }
                ds.push({ label: name + ' RX', data: rxData, borderColor: c.rx, backgroundColor: 'transparent', fill: false, tension: 0.3, cubicInterpolationMode: 'monotone', pointRadius: 0, borderWidth: 1.5 });
                ds.push({ label: name + ' TX', data: txData, borderColor: c.tx, backgroundColor: 'transparent', fill: false, tension: 0.3, cubicInterpolationMode: 'monotone', pointRadius: 0, borderWidth: 1.5 });
                ci++;
            }
            historyChart.data.datasets = ds;
            historyChart.update('none');
            // Update subtitle with data coverage
            var subEl = document.querySelector('#historyChart').closest('.card').querySelector('.card-subtitle');
            if (subEl && earliestTs < Infinity) {
                var ageMs = Date.now() - earliestTs;
                var ageH = Math.floor(ageMs / 3600000);
                var ageM = Math.floor((ageMs % 3600000) / 60000);
                var ageStr = ageH > 0 ? ageH + 'h ' + ageM + 'm' : ageM + 'm';
                subEl.textContent = ageH >= 24 ? 'Per-interface bandwidth over the last 24 hours' : 'Collecting data \u2014 ' + ageStr + ' of 24h available';
            }
        }).catch(function(e) { console.error('history load:', e); });
    }
    function loadInterfaceHistory() {
        if (_historyLoaded) return;
        _historyLoaded = true;
        _fetchInterfaceHistory();
        // Refresh the 24h history chart every 60 seconds
        _historyRefreshInterval = setInterval(_fetchInterfaceHistory, 60000);
    }

    function makeDoughnut(id) {
        return new Chart(document.getElementById(id).getContext('2d'), {
            type: 'doughnut',
            data: { labels: [], datasets: [{ data: [], backgroundColor: [], borderWidth: 0 }] },
            options: {
                responsive: true, maintainAspectRatio: true, cutout: '65%', animation: false,
                plugins: {
                    legend: { display: false },
                    tooltip: {
                        backgroundColor: '#18181b', titleColor: '#fafafa', bodyColor: '#a1a1aa',
                        borderColor: '#27272a', borderWidth: 1,
                        callbacks: { label: function(c) { return c.label + ': ' + (c.raw >= 1024 ? formatBytes(c.raw) : c.raw.toLocaleString()); } }
                    }
                }
            }
        });
    }

    var protoChart = makeDoughnut('protoChart');
    var countryChart = makeDoughnut('countryChart');
    var asnChart = makeDoughnut('asnChart');
    var ipvChart = makeDoughnut('ipvChart');
    var ssidChart = makeDoughnut('ssidChart');
    var apClientsChart = makeDoughnut('apClientsChart');
    var apTrafficChart = makeDoughnut('apTrafficChart');
    var ssidTrafficChart = makeDoughnut('ssidTrafficChart');
    var dnsClientsChart = makeDoughnut('dnsClientsChart');
    var dnsDomainsChart = makeDoughnut('dnsDomainsChart');
    var dnsBlockedDomainsChart = makeDoughnut('dnsBlockedDomainsChart');
    var natProtoChart = makeDoughnut('natProtoChart');
    var natStateChart = makeDoughnut('natStateChart');
    var natTypeChart = makeDoughnut('natTypeChart');
    var natIPvChart = makeDoughnut('natIPvChart');

    // ── Chart theme sync ──
    window._updateChartsForTheme = function() {
        var s = getComputedStyle(document.documentElement);
        var grid = s.getPropertyValue('--bg-3').trim();
        var tick = s.getPropertyValue('--text-2').trim();
        var bdr = s.getPropertyValue('--border').trim();
        var bg2 = s.getPropertyValue('--bg-2').trim();
        var t0 = s.getPropertyValue('--text-0').trim();
        var t1 = s.getPropertyValue('--text-1').trim();
        trafficChart.options.scales.x.grid.color = grid;
        trafficChart.options.scales.x.ticks.color = tick;
        trafficChart.options.scales.x.border.color = bdr;
        trafficChart.options.scales.y.grid.color = grid;
        trafficChart.options.scales.y.ticks.color = tick;
        trafficChart.options.scales.y.border.color = bdr;
        trafficChart.options.plugins.legend.labels.color = tick;
        trafficChart.options.plugins.tooltip.backgroundColor = bg2;
        trafficChart.options.plugins.tooltip.titleColor = t0;
        trafficChart.options.plugins.tooltip.bodyColor = t1;
        trafficChart.options.plugins.tooltip.borderColor = bdr;
        trafficChart.update('none');
        [protoChart, countryChart, asnChart, ipvChart, ssidChart, apClientsChart, apTrafficChart, ssidTrafficChart, dnsClientsChart, dnsDomainsChart, dnsBlockedDomainsChart, natProtoChart, natStateChart, natTypeChart, natIPvChart].forEach(function(ch) {
            ch.options.plugins.tooltip.backgroundColor = bg2;
            ch.options.plugins.tooltip.titleColor = t0;
            ch.options.plugins.tooltip.bodyColor = t1;
            ch.options.plugins.tooltip.borderColor = bdr;
            ch.update('none');
        });
    };
    window._updateChartsForTheme();

    window._toggleDetail = function(which) {
        var detail = document.getElementById(which + 'Detail');
        var toggle = document.getElementById(which + 'Toggle');
        var isOpen = detail.classList.contains('open');
        detail.classList.toggle('open');
        toggle.classList.toggle('open');
        toggle.querySelector('span').textContent = isOpen ? 'Show details' : 'Hide details';
    };

    var selectedIface = null;
    var knownIfaces = new Set();
    var MAX_PTS = 3600;
    var chartData = {};
    var sparklineData = {};
    var _emaState = {}; // EMA smoothing state per interface
    var EMA_ALPHA = 0.3; // 0.3 = responsive but smooth (higher = less smoothing)
    var _yAxisMax = 0; // high-water mark for Y-axis stability
    var _yAxisDecay = 0.995; // slow decay per update (~5s half-life at 1Hz)

    function updateChart() {
        var ds = [], ci = 0;
        var list = selectedIface ? [selectedIface] : Array.from(knownIfaces);
        for (var n of list) {
            if (!chartData[n]) continue;
            var c = chartColors[ci % chartColors.length];
            ds.push({ label: n + ' RX', data: chartData[n].rx, borderColor: c.rx, backgroundColor: c.rxBg, fill: false, tension: 0.4, pointRadius: 0, borderWidth: 1.5 });
            ds.push({ label: n + ' TX', data: chartData[n].tx, borderColor: c.tx, backgroundColor: c.txBg, fill: false, tension: 0.4, pointRadius: 0, borderWidth: 1.5 });
            ci++;
        }
        trafficChart.data.datasets = ds;
        // Stabilize Y-axis: use a high-water mark that decays slowly.
        // This prevents the scale from jumping on every update.
        var currentMax = 0;
        for (var di = 0; di < ds.length; di++) {
            var pts = ds[di].data;
            for (var pi = Math.max(0, pts.length - 120); pi < pts.length; pi++) {
                var av = Math.abs(pts[pi].y);
                if (av > currentMax) currentMax = av;
            }
        }
        _yAxisMax = Math.max(currentMax * 1.15, _yAxisMax * _yAxisDecay);
        if (_yAxisMax > 0) {
            trafficChart.options.scales.y.suggestedMax = _yAxisMax;
            trafficChart.options.scales.y.suggestedMin = -_yAxisMax;
        }
        trafficChart.update('none');
    }

    function renderIfaceTabs() {
        var el = document.getElementById('ifaceTabs');
        var h = '<div class="iface-tab' + (selectedIface === null ? ' active' : '') + '" onclick="window._si(null)">All</div>';
        knownIfaces.forEach(function(n) {
            h += '<div class="iface-tab' + (selectedIface === n ? ' active' : '') + '" onclick="window._si(\'' + n + '\')">' + n + '</div>';
        });
        el.innerHTML = h;
    }

    window._si = function(n) { selectedIface = n; renderIfaceTabs(); updateChart(); };

    // Use the backend-provided iface_type; fall back to name heuristics.
    // Interfaces tagged as WAN by the server are grouped under 'wan'.
    function classifyIface(f) {
        if (f.wan) return 'wan';
        if (f.iface_type) return f.iface_type;
        var n = (f.name || '').toLowerCase();
        if (/^(tun|tap|wg|ipsec|gre|vti|ovpn)/.test(n)) return 'vpn';
        if (/\.\d+$/.test(n) || /^vlan/.test(n)) return 'vlan';
        return 'physical';
    }

    var groupMeta = {
        wan:      { label: 'WAN', order: 0 },
        vpn:      { label: 'VPN', order: 1 },
        vlan:     { label: 'VLAN', order: 2 },
        physical: { label: 'Physical', order: 3 },
        loopback: { label: 'Loopback', order: 4 }
    };

    function renderSystemCard(ifaces, d) {
        var totalRxRate = 0, totalTxRate = 0, totalRxBytes = 0, totalTxBytes = 0;
        for (var f of ifaces) {
            totalRxRate += f.rx_rate || 0;
            totalTxRate += f.tx_rate || 0;
            totalRxBytes += f.rx_bytes || 0;
            totalTxBytes += f.tx_bytes || 0;
        }
        var el = document.getElementById('systemStats');
        var sub = document.getElementById('systemSubtitle');
        if (!el) return;
        function stat(label, value, cls) {
            return '<div style="padding:8px 4px"><div style="font-size:10px;color:var(--text-2);margin-bottom:4px">' + label + '</div><div style="font-size:15px;font-weight:700;font-variant-numeric:tabular-nums' + (cls ? ';color:var(--' + cls + ')' : '') + '">' + value + '</div></div>';
        }
        var h = '';
        h += stat('Uptime', d && d.uptime_secs ? formatUptime(d.uptime_secs) : '—');
        if (d && d.load_avg) {
            h += stat('Load 1m', d.load_avg[0].toFixed(2));
            h += stat('Load 5m', d.load_avg[1].toFixed(2));
            h += stat('Load 15m', d.load_avg[2].toFixed(2));
        }
        h += stat('Processes', d && d.processes ? d.processes.running + ' / ' + d.processes.total : '—');
        h += stat('Bandwidth', formatRate(totalRxRate + totalTxRate));
        h += stat('RX Rate', formatRate(totalRxRate), 'rx');
        h += stat('TX Rate', formatRate(totalTxRate), 'tx');
        h += stat('Total RX', formatBytes(totalRxBytes), 'rx');
        h += stat('Total TX', formatBytes(totalTxBytes), 'tx');
        h += stat('IPs (24h)', d && d.unique_ips ? (d.unique_ips).toLocaleString() : '—');
        el.innerHTML = h;
        if (sub && d && d.uptime_secs) {
            sub.textContent = ifaces.length + ' interfaces · up ' + formatUptime(d.uptime_secs);
        }
    }

    function renderStatsRow(ifaces, d) {
        renderSystemCard(ifaces, d);
        var groups = {};
        for (var f of ifaces) {
            var g = classifyIface(f);
            if (!groups[g]) groups[g] = { rx: 0, tx: 0, count: 0 };
            groups[g].rx += f.rx_rate || 0;
            groups[g].tx += f.tx_rate || 0;
            groups[g].count++;
        }
        var keys = Object.keys(groups).sort(function(a, b) {
            return (groupMeta[a] ? groupMeta[a].order : 99) - (groupMeta[b] ? groupMeta[b].order : 99);
        });
        var h = '';
        for (var i = 0; i < keys.length; i++) {
            var k = keys[i], meta = groupMeta[k] || { label: k }, grp = groups[k];
            h += '<div class="stats-group">';
            h += '<div class="stats-group-header">' + meta.label + '<span>' + grp.count + '</span></div>';
            h += '<div class="stats-group-body">';
            h += '<div><div class="stat-mini-label">RX</div><div class="stat-mini-value rx">' + formatRate(grp.rx) + '</div></div>';
            h += '<div><div class="stat-mini-label">TX</div><div class="stat-mini-value tx">' + formatRate(grp.tx) + '</div></div>';
            h += '</div></div>';
        }
        document.getElementById('statsRow').innerHTML = h;
    }

    function renderIfaceCard(f, groupLabel) {
        var hasErr = (f.rx_errors || 0) + (f.tx_errors || 0) > 0;
        var hasDrop = (f.rx_dropped || 0) + (f.tx_dropped || 0) > 0;
        var os = f.oper_state || 'unknown';
        var dotClass = (os === 'up') ? 'up' : (os === 'down' ? 'down' : 'unknown');
        var stateLabel = os === 'up' ? 'Up' : (os === 'down' ? 'Down' : os);
        var speedLabel = (f.speed && f.speed > 0) ? '<span style="font-size:10px;color:var(--text-2);font-weight:400;margin-right:4px">' + (f.speed >= 1000 ? (f.speed / 1000) + ' Gbit' : f.speed + ' Mbit') + '</span>' : '';
        var badge = groupLabel ? '<span class="iface-group-badge">' + groupLabel + '</span>' : '';
        var h = '<div class="iface-card"><div class="iface-name"><span>' + f.name + ' ' + badge + '</span><span class="iface-status">' + speedLabel + '<span class="iface-status-dot ' + dotClass + '"></span>' + stateLabel + '</span></div>';
        h += '<div class="sparkline-wrap"><canvas class="sparkline-canvas" data-iface="' + f.name + '"></canvas></div>';
        if (f.vpn_routing) {
            h += '<div class="vpn-routing active"><span class="iface-status-dot up"></span>Routing' + (f.vpn_routing_since ? ' since ' + f.vpn_routing_since : '') + '</div>';
        } else if (f.iface_type === 'vpn' && f.vpn_tracked) {
            h += '<div class="vpn-routing inactive">Not routing</div>';
        }
        h += '<div class="iface-stats">';
        h += '<div><div class="iface-stat-label label-rx">RX Rate</div><div class="iface-stat-value" style="color:var(--rx)">' + formatRate(f.rx_rate || 0) + '</div></div>';
        h += '<div><div class="iface-stat-label label-tx">TX Rate</div><div class="iface-stat-value" style="color:var(--tx)">' + formatRate(f.tx_rate || 0) + '</div></div>';
        h += '<div><div class="iface-stat-label">RX Total</div><div class="iface-stat-value">' + formatBytes(f.rx_bytes || 0) + '</div></div>';
        h += '<div><div class="iface-stat-label">TX Total</div><div class="iface-stat-value">' + formatBytes(f.tx_bytes || 0) + '</div></div>';
        h += '<div><div class="iface-stat-label">RX Pkts</div><div class="iface-stat-value">' + formatPPS(f.rx_pps || 0) + '</div></div>';
        h += '<div><div class="iface-stat-label">TX Pkts</div><div class="iface-stat-value">' + formatPPS(f.tx_pps || 0) + '</div></div>';
        if (hasErr || (f.rx_error_rate || 0) + (f.tx_error_rate || 0) > 0) h += '<div><div class="iface-stat-label label-err">Errors</div><div class="iface-stat-value" style="color:var(--danger)">' + (f.rx_error_rate > 0 || f.tx_error_rate > 0 ? formatPPS(f.rx_error_rate || 0) + ' / ' + formatPPS(f.tx_error_rate || 0) + '/s' : f.rx_errors + ' / ' + f.tx_errors) + '</div></div>';
        if (hasDrop || (f.rx_drop_rate || 0) + (f.tx_drop_rate || 0) > 0) h += '<div><div class="iface-stat-label">Drops</div><div class="iface-stat-value" style="color:var(--warning)">' + (f.rx_drop_rate > 0 || f.tx_drop_rate > 0 ? formatPPS(f.rx_drop_rate || 0) + ' / ' + formatPPS(f.tx_drop_rate || 0) + '/s' : f.rx_dropped + ' / ' + f.tx_dropped) + '</div></div>';
        h += '</div>';
        if (f.addrs && f.addrs.length) {
            var v4 = [], v6 = [];
            for (var ai = 0; ai < f.addrs.length; ai++) {
                if (f.addrs[ai].indexOf(':') !== -1) v6.push(f.addrs[ai]); else v4.push(f.addrs[ai]);
            }
            h += '<div class="iface-addrs">';
            if (v4.length) h += '<div class="iface-addr-row"><span class="iface-addr-tag v4">IPv4</span><span class="iface-addr-list">' + v4.join(', ') + '</span></div>';
            if (v6.length) h += '<div class="iface-addr-row"><span class="iface-addr-tag v6">IPv6</span><span class="iface-addr-list">' + v6.join(', ') + '</span></div>';
            h += '</div>';
        }
        h += '</div>';
        return h;
    }

    function renderIfaceCards(ifaces) {
        var el = document.getElementById('ifaceGroups');
        if (!ifaces || !ifaces.length) { el.innerHTML = ''; return; }

        // Group interfaces by type
        var groups = {};
        for (var f of ifaces) {
            var g = classifyIface(f);
            if (!groups[g]) groups[g] = [];
            groups[g].push(f);
        }

        // Sort each group internally (lo last)
        for (var k in groups) {
            groups[k].sort(function(a, b) {
                if (a.name === 'lo') return 1;
                if (b.name === 'lo') return -1;
                return a.name.localeCompare(b.name);
            });
        }

        // Render all cards in a single grid with group badges
        var keys = Object.keys(groups).sort(function(a, b) {
            return (groupMeta[a] ? groupMeta[a].order : 99) - (groupMeta[b] ? groupMeta[b].order : 99);
        });

        var h = '<div class="iface-cards">';
        for (var gi = 0; gi < keys.length; gi++) {
            var gk = keys[gi];
            var meta = groupMeta[gk] || { label: gk };
            for (var fi = 0; fi < groups[gk].length; fi++) {
                h += renderIfaceCard(groups[gk][fi], meta.label);
            }
        }
        h += '</div>';
        el.innerHTML = h;
    }

    function renderTalkers(tid, talkers, vk, fmt, cls) {
        var tb = document.getElementById(tid);
        if (!talkers || !talkers.length) { tb.innerHTML = '<tr><td colspan="5" class="empty-state">No data &mdash; requires root / CAP_NET_RAW</td></tr>'; return; }

        // Detect if LOCAL_NETS direction data is present
        var hasDirection = false;
        for (var di = 0; di < talkers.length; di++) {
            if ((talkers[di].rx_bytes || 0) > 0 || (talkers[di].tx_bytes || 0) > 0) { hasDirection = true; break; }
        }

        var mx = talkers[0][vk] || 1, h = '';
        var isRate = (vk === 'rate_bytes');
        talkers.forEach(function(t, i) {
            var pct = ((t[vk] / mx) * 100).toFixed(1);
            var flag = t.country ? countryFlag(t.country) + ' ' : '';
            var geo = '';
            var geoName = (t.city && t.country_name) ? t.city + ', ' + t.country_name : (t.country_name || '');
            if (t.as_org) geo = '<span class="hostname">' + flag + geoName + ' &middot; AS' + (t.asn || '') + ' ' + t.as_org + '</span>';
            else if (geoName) geo = '<span class="hostname">' + flag + geoName + '</span>';
            var host = t.hostname && t.hostname !== t.ip
                ? '<span class="ip-cell ip-clickable" data-ip="' + t.ip + '">' + t.ip + '</span><span class="hostname">' + t.hostname + '</span>' + geo
                : '<span class="ip-cell ip-clickable" data-ip="' + t.ip + '">' + t.ip + '</span>' + geo;
            h += '<tr><td><span class="' + rankClass(i) + '">' + (i + 1) + '</span></td>';
            h += '<td>' + host + '</td>';
            if (hasDirection) {
                if (isRate) {
                    h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--rx)">' + formatRate(t.rx_rate || 0) + '</td>';
                    h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--tx)">' + formatRate(t.tx_rate || 0) + '</td>';
                } else {
                    h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--rx)">' + formatBytes(t.rx_bytes || 0) + '</td>';
                    h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--tx)">' + formatBytes(t.tx_bytes || 0) + '</td>';
                }
            }
            h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums">' + fmt(t[vk]) + '</td>';
            h += '<td class="bar-cell"><div class="bar-bg"></div><div class="bar-fill ' + cls + '" style="width:' + pct + '%"></div></td></tr>';
        });

        // Update table header dynamically
        var thead = tb.parentElement.querySelector('thead tr');
        if (thead) {
            if (hasDirection) {
                thead.innerHTML = '<th>#</th><th>Host</th><th style="width:12%">RX</th><th style="width:12%">TX</th><th style="width:12%">Total</th><th style="width:18%"></th>';
            } else {
                thead.innerHTML = '<th>#</th><th>Host</th><th>' + (isRate ? 'Rate' : 'Total') + '</th><th style="width:28%"></th>';
            }
        }
        tb.innerHTML = h;
    }

    function drawSparkline(canvas, points) {
        if (!points || points.length < 2 || !canvas) return;
        var dpr = window.devicePixelRatio || 1;
        var dw = canvas.offsetWidth, dh = canvas.offsetHeight;
        if (!dw || !dh) return;
        canvas.width = dw * dpr; canvas.height = dh * dpr;
        var c = canvas.getContext('2d'); c.scale(dpr, dpr);
        var maxVal = 0;
        for (var i = 0; i < points.length; i++) { if (points[i].rx > maxVal) maxVal = points[i].rx; if (points[i].tx > maxVal) maxVal = points[i].tx; }
        if (maxVal === 0) return;
        var stepX = dw / (points.length - 1), pad = 2, usableH = dh - pad * 2;
        function getY(val) { return dh - pad - (val / maxVal) * usableH; }
        function drawArea(key, fill, stroke) {
            var pts = [];
            for (var j = 0; j < points.length; j++) pts.push({ x: j * stepX, y: getY(points[j][key]) });
            // Smooth filled area
            c.beginPath(); c.moveTo(0, dh);
            c.lineTo(pts[0].x, pts[0].y);
            for (var j = 1; j < pts.length; j++) {
                var cx = (pts[j-1].x + pts[j].x) / 2;
                c.bezierCurveTo(cx, pts[j-1].y, cx, pts[j].y, pts[j].x, pts[j].y);
            }
            c.lineTo(pts[pts.length-1].x, dh); c.closePath(); c.fillStyle = fill; c.fill();
            // Smooth line
            c.beginPath(); c.moveTo(pts[0].x, pts[0].y);
            for (var j = 1; j < pts.length; j++) {
                var cx = (pts[j-1].x + pts[j].x) / 2;
                c.bezierCurveTo(cx, pts[j-1].y, cx, pts[j].y, pts[j].x, pts[j].y);
            }
            c.strokeStyle = stroke; c.lineWidth = 1.5; c.stroke();
        }
        drawArea('tx', 'rgba(167,139,250,0.15)', 'rgba(167,139,250,0.5)');
        drawArea('rx', 'rgba(34,211,238,0.15)', 'rgba(34,211,238,0.5)');
    }

    function drawAllSparklines() {
        var els = document.querySelectorAll('.sparkline-canvas');
        for (var i = 0; i < els.length; i++) drawSparkline(els[i], sparklineData[els[i].getAttribute('data-iface')]);
    }

    function updateProtoChart(data) {
        if (!data) return;
        var order = ['TCP', 'UDP', 'ICMP', 'Other'], labels = [], values = [], colors = [];
        for (var i = 0; i < order.length; i++) { if (data[order[i]]) { labels.push(order[i]); values.push(data[order[i]]); colors.push(protoColors[order[i]]); } }
        for (var k in data) { if (order.indexOf(k) === -1) { labels.push(k); values.push(data[k]); colors.push('#71717a'); } }
        updateSimpleDoughnut(protoChart, document.getElementById('protoLegend'), labels, values, colors, formatBytes);
    }

    function updateCountries(countries) {
        var tb = document.getElementById('countryTable');
        var legend = document.getElementById('countryLegend');
        if (!countries || !countries.length) {
            tb.innerHTML = '<tr><td colspan="5" class="empty-state">No GeoIP data &mdash; place MMDB files next to binary</td></tr>';
            countryChart.data.labels = []; countryChart.data.datasets[0].data = []; countryChart.update('none');
            legend.innerHTML = '<div style="text-align:center;color:var(--text-2);font-size:12px;padding:8px">No data</div>';
            return;
        }
        fillDoughnut(countryChart, legend, countries,
            function(c) { return countryFlag(c.country) + ' ' + (c.country_name || c.country); },
            function(c) { return c.bytes; },
            function(v) { return formatBytes(v); }
        );
        fillDetailTable('countryTable', countries,
            function(c) { return countryFlag(c.country) + ' <span style="font-weight:500">' + (c.country_name || c.country) + '</span> <span class="hostname" style="display:inline">' + c.country + '</span>'; },
            function(c) { return c.bytes; },
            function(v) { return formatBytes(v); }, 'bw'
        );
    }

    var ipvColors = { 'IPv4': '#60a5fa', 'IPv6': '#a855f7' };

    function updateIPVersions(data) {
        var legend = document.getElementById('ipvLegend');
        if (!data) {
            ipvChart.data.labels = []; ipvChart.data.datasets[0].data = []; ipvChart.update('none');
            legend.innerHTML = '<div style="text-align:center;color:var(--text-2);font-size:12px;padding:8px">No data</div>';
            return;
        }
        var labels = [], values = [], colors = [];
        if (data['IPv4']) { labels.push('IPv4'); values.push(data['IPv4']); colors.push(ipvColors['IPv4']); }
        if (data['IPv6']) { labels.push('IPv6'); values.push(data['IPv6']); colors.push(ipvColors['IPv6']); }
        updateSimpleDoughnut(ipvChart, legend, labels, values, colors, formatBytes);
    }

    function updateASNs(asns) {
        var tb = document.getElementById('asnTable');
        var legend = document.getElementById('asnLegend');
        if (!asns || !asns.length) {
            tb.innerHTML = '<tr><td colspan="5" class="empty-state">No ASN data &mdash; place GeoLite2-ASN.mmdb next to binary</td></tr>';
            asnChart.data.labels = []; asnChart.data.datasets[0].data = []; asnChart.update('none');
            legend.innerHTML = '<div style="text-align:center;color:var(--text-2);font-size:12px;padding:8px">No data</div>';
            return;
        }
        fillDoughnut(asnChart, legend, asns,
            function(a) { return a.as_org || 'AS' + a.asn; },
            function(a) { return a.bytes; },
            function(v) { return formatBytes(v); }
        );
        fillDetailTable('asnTable', asns,
            function(a) { return '<span style="font-weight:500">' + (a.as_org || 'Unknown') + '</span> <span class="hostname" style="display:inline">AS' + a.asn + '</span>'; },
            function(a) { return a.bytes; },
            function(v) { return formatBytes(v); }, 'vol'
        );
    }

    var sse = null;

    // DNS mini-bar charts
    function makeBarChart(canvasId, color) {
        return new Chart(document.getElementById(canvasId).getContext('2d'), {
            type: 'bar',
            data: { labels: [], datasets: [{ data: [], backgroundColor: color, borderWidth: 0, borderRadius: 1 }] },
            options: {
                responsive: true, maintainAspectRatio: false, animation: false,
                plugins: { legend: { display: false }, tooltip: { enabled: false } },
                scales: {
                    x: { display: false },
                    y: { display: false, beginAtZero: true }
                }
            }
        });
    }
    var dnsQChart = null, dnsBChart = null;

    function updateDNS(dns) {
        if (!dns) return;
        document.getElementById('dnsNoData').style.display = 'none';
        document.getElementById('dnsHasData').style.display = '';

        var providerName = dns.provider_name || 'DNS';
        document.getElementById('dnsCardTitle').textContent = 'DNS \u2014 ' + providerName;

        document.getElementById('dnsTotalQueries').textContent = dns.total_queries.toLocaleString();
        document.getElementById('dnsBlocked').textContent = dns.blocked_total.toLocaleString();
        document.getElementById('dnsBlockPct').textContent = dns.blocked_pct.toFixed(1) + '%';
        document.getElementById('dnsLatency').textContent = dns.avg_latency_ms.toFixed(1) + ' ms';

        // Queries time-series bar chart
        if (dns.queries_series && dns.queries_series.length) {
            if (!dnsQChart) dnsQChart = makeBarChart('dnsQueriesChart', 'rgba(59,130,246,0.6)');
            var labels = [];
            for (var i = 0; i < dns.queries_series.length; i++) labels.push(i);
            dnsQChart.data.labels = labels;
            dnsQChart.data.datasets[0].data = dns.queries_series;
            dnsQChart.update('none');
        }

        // Blocked time-series bar chart
        if (dns.blocked_series && dns.blocked_series.length) {
            if (!dnsBChart) dnsBChart = makeBarChart('dnsBlockedChart', 'rgba(239,68,68,0.6)');
            var labels2 = [];
            for (var i = 0; i < dns.blocked_series.length; i++) labels2.push(i);
            dnsBChart.data.labels = labels2;
            dnsBChart.data.datasets[0].data = dns.blocked_series;
            dnsBChart.update('none');
        }

        // ── Top DNS Clients pie chart ──
        fillDoughnut(dnsClientsChart, document.getElementById('dnsClientsLegend'),
            dns.top_clients || [],
            function(c) { return c.ip || 'Unknown'; },
            function(c) { return c.count || 0; },
            function(v) { return v.toLocaleString() + ' queries'; }
        );
        fillDetailTable('dnsClientsTable', dns.top_clients || [],
            function(c) { return c.ip || 'Unknown'; },
            function(c) { return c.count || 0; },
            function(v) { return v.toLocaleString(); }, 'bw'
        );

        // ── Top Queried Domains pie chart ──
        fillDoughnut(dnsDomainsChart, document.getElementById('dnsDomainsLegend'),
            dns.top_queried || [],
            function(d) { return d.domain || '—'; },
            function(d) { return d.count || 0; },
            function(v) { return v.toLocaleString() + ' queries'; }
        );
        fillDetailTable('dnsDomainsTable', dns.top_queried || [],
            function(d) { return d.domain || '—'; },
            function(d) { return d.count || 0; },
            function(v) { return v.toLocaleString(); }, 'bw'
        );

        // ── Top Blocked Domains pie chart ──
        fillDoughnut(dnsBlockedDomainsChart, document.getElementById('dnsBlockedDomainsLegend'),
            dns.top_blocked || [],
            function(d) { return d.domain || '—'; },
            function(d) { return d.count || 0; },
            function(v) { return v.toLocaleString() + ' blocked'; }
        );
        fillDetailTable('dnsBlockedDomainsTable', dns.top_blocked || [],
            function(d) { return d.domain || '—'; },
            function(d) { return d.count || 0; },
            function(v) { return v.toLocaleString(); }, 'vol'
        );

        // ── Upstream DNS Servers table ──
        var upTb = document.getElementById('dnsUpstreamTable');
        var ups = dns.upstreams || [];
        if (!ups.length) {
            upTb.innerHTML = '<tr><td colspan="5" class="empty-state">No upstream data</td></tr>';
        } else {
            var totalResp = 0;
            for (var i = 0; i < ups.length; i++) totalResp += ups[i].responses || 0;
            var uh = '';
            for (var i = 0; i < ups.length; i++) {
                var u = ups[i];
                var pct = totalResp > 0 ? ((u.responses / totalResp) * 100) : 0;
                var latStr = u.avg_ms > 0 ? u.avg_ms.toFixed(1) + ' ms' : '—';
                var barCls = u.avg_ms <= 20 ? 'bw' : (u.avg_ms <= 100 ? 'vol' : 'vol');
                uh += '<tr><td class="rank-cell">' + (i + 1) + '</td>';
                uh += '<td class="host-cell" title="' + u.address + '"><code>' + u.address + '</code></td>';
                uh += '<td>' + (u.responses || 0).toLocaleString() + '</td>';
                uh += '<td>' + latStr + '</td>';
                uh += '<td class="bar-cell"><div class="bar-bg"></div><div class="bar-fill ' + barCls + '" style="width:' + pct.toFixed(1) + '%"></div></td></tr>';
            }
            upTb.innerHTML = uh;
        }
    }

    // Helper: populate a doughnut chart + legend + optional detail table
    function fillDoughnut(chart, legendEl, items, labelFn, valueFn, fmtFn) {
        if (!items || !items.length) {
            chart.data.labels = []; chart.data.datasets[0].data = []; chart.update('none');
            legendEl.innerHTML = '<div style="text-align:center;color:var(--text-2);font-size:12px;padding:8px">No data</div>';
            return;
        }
        var chartMax = 8, chartLabels = [], chartValues = [], chartColors = [], rest = 0;
        var total = 0;
        for (var i = 0; i < items.length; i++) total += valueFn(items[i]);
        for (var i = 0; i < items.length; i++) {
            if (i < chartMax) {
                chartLabels.push(labelFn(items[i]));
                chartValues.push(valueFn(items[i]));
                chartColors.push(geoChartPalette[i % geoChartPalette.length]);
            } else { rest += valueFn(items[i]); }
        }
        if (rest > 0) { chartLabels.push('Others'); chartValues.push(rest); chartColors.push('#52525b'); }
        chart.data.labels = chartLabels;
        chart.data.datasets[0].data = chartValues;
        chart.data.datasets[0].backgroundColor = chartColors;
        chart.update('none');
        var lh = '', legendCount = Math.min(items.length, chartMax);
        for (var i = 0; i < legendCount; i++) {
            var pct = total > 0 ? ((valueFn(items[i]) / total) * 100).toFixed(1) : '0.0';
            lh += '<div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">';
            lh += '<div style="width:8px;height:8px;border-radius:2px;background:' + chartColors[i] + ';flex-shrink:0"></div>';
            lh += '<div style="flex:1;min-width:0"><div style="font-size:12px;font-weight:500;color:var(--text-0);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + labelFn(items[i]) + '</div>';
            lh += '<div style="font-size:10px;color:var(--text-2)">' + fmtFn(valueFn(items[i])) + ' &middot; ' + pct + '%</div></div></div>';
        }
        if (rest > 0) {
            var rpct = total > 0 ? ((rest / total) * 100).toFixed(1) : '0.0';
            lh += '<div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">';
            lh += '<div style="width:8px;height:8px;border-radius:2px;background:#52525b;flex-shrink:0"></div>';
            lh += '<div style="flex:1"><div style="font-size:12px;font-weight:500;color:var(--text-0)">Others</div>';
            lh += '<div style="font-size:10px;color:var(--text-2)">' + fmtFn(rest) + ' &middot; ' + rpct + '%</div></div></div>';
        }
        legendEl.innerHTML = lh;
    }

    function fillDetailTable(tbId, items, labelFn, valueFn, fmtFn, cls) {
        var tb = document.getElementById(tbId);
        if (!items || !items.length) { tb.innerHTML = '<tr><td colspan="4" class="empty-state">No data</td></tr>'; return; }
        var mx = valueFn(items[0]) || 1, h = '';
        for (var i = 0; i < items.length; i++) {
            var pct = mx > 0 ? ((valueFn(items[i]) / mx) * 100).toFixed(1) : '0';
            h += '<tr><td><span class="' + rankClass(i) + '">' + (i + 1) + '</span></td>';
            h += '<td><span class="ip-cell">' + labelFn(items[i]) + '</span></td>';
            h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums">' + fmtFn(valueFn(items[i])) + '</td>';
            h += '<td class="bar-cell"><div class="bar-bg"></div><div class="bar-fill ' + cls + '" style="width:' + pct + '%"></div></td></tr>';
        }
        tb.innerHTML = h;
    }

    function fillTrafficDetailTable(tbId, items, labelFn) {
        var tb = document.getElementById(tbId);
        if (!items || !items.length) { tb.innerHTML = '<tr><td colspan="7" class="empty-state">No data</td></tr>'; return; }
        var mx = 1;
        for (var i = 0; i < items.length; i++) { var t = (items[i].tx_bytes || 0) + (items[i].rx_bytes || 0); if (t > mx) mx = t; }
        var h = '';
        for (var i = 0; i < items.length; i++) {
            var it = items[i], total = (it.tx_bytes || 0) + (it.rx_bytes || 0);
            var pct = mx > 0 ? ((total / mx) * 100).toFixed(1) : '0';
            h += '<tr><td><span class="' + rankClass(i) + '">' + (i + 1) + '</span></td>';
            h += '<td><span class="ip-cell">' + labelFn(it) + '</span></td>';
            h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums">' + formatBytes(it.rx_bytes || 0) + '</td>';
            h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums">' + formatBytes(it.tx_bytes || 0) + '</td>';
            h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--rx)">' + formatRate(it.rx_rate || 0) + '</td>';
            h += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--tx)">' + formatRate(it.tx_rate || 0) + '</td>';
            h += '<td class="bar-cell"><div class="bar-bg"></div><div class="bar-fill bw" style="width:' + pct + '%"></div></td></tr>';
        }
        tb.innerHTML = h;
    }

    // ── NAT / Conntrack ──
    var _currentNatVersion = 'v4';
    var _lastConntrack = null;

    window._switchNatVersion = function(ver) {
        _currentNatVersion = ver;
        document.querySelectorAll('.nat-version-tab').forEach(function(t) {
            t.classList.toggle('active', t.getAttribute('data-ver') === ver);
        });
        var title = document.getElementById('natTableTitle');
        var sub = document.getElementById('natTableSubtitle');
        if (ver === 'v4') {
            title.textContent = 'IPv4 NAT Translations';
            sub.textContent = 'Active conntrack entries (top 200 by traffic volume)';
        } else {
            title.textContent = 'IPv6 NAT Translations';
            sub.textContent = 'Active conntrack entries (top 200 by traffic volume)';
        }
        if (_lastConntrack) renderNATEntries(_lastConntrack);
    };

    var natProtoColors = { 'TCP': '#3b82f6', 'UDP': '#22d3ee', 'ICMP': '#f59e0b', 'SCTP': '#34d399', 'Other': '#71717a' };
    var natStateColors = {
        'ESTABLISHED': '#22c55e', 'TIME_WAIT': '#eab308', 'SYN_SENT': '#3b82f6',
        'SYN_RECV': '#60a5fa', 'FIN_WAIT': '#f59e0b', 'CLOSE_WAIT': '#ef4444',
        'CLOSE': '#f87171', 'LAST_ACK': '#fb923c', 'NONE': '#71717a'
    };
    var natTypeColors = { 'snat': '#22d3ee', 'dnat': '#a78bfa', 'both': '#fb923c', 'none': '#71717a' };

    function updateNAT(ct) {
        if (!ct) return;
        _lastConntrack = ct;
        document.getElementById('natNoData').style.display = 'none';
        document.getElementById('natHasData').style.display = '';

        document.getElementById('natTotal').textContent = (ct.total || 0).toLocaleString();
        document.getElementById('natMax').textContent = (ct.max || 0).toLocaleString();
        document.getElementById('natUsagePct').textContent = (ct.usage_pct || 0).toFixed(1) + '%';
        document.getElementById('natIPv4').textContent = (ct.ipv4 || 0).toLocaleString();
        document.getElementById('natIPv6').textContent = (ct.ipv6 || 0).toLocaleString();

        // Protocol doughnut
        var protocols = ct.protocols || {};
        var pLabels = [], pValues = [], pColors = [];
        var pOrder = ['TCP', 'UDP', 'ICMP'];
        for (var i = 0; i < pOrder.length; i++) {
            if (protocols[pOrder[i]]) { pLabels.push(pOrder[i]); pValues.push(protocols[pOrder[i]]); pColors.push(natProtoColors[pOrder[i]] || '#71717a'); }
        }
        for (var k in protocols) { if (pOrder.indexOf(k) === -1) { pLabels.push(k); pValues.push(protocols[k]); pColors.push(natProtoColors['Other']); } }
        updateSimpleDoughnut(natProtoChart, document.getElementById('natProtoLegend'), pLabels, pValues, pColors);

        // State doughnut
        var states = ct.states || {};
        var sLabels = [], sValues = [], sColors = [];
        var sOrder = ['ESTABLISHED', 'TIME_WAIT', 'SYN_SENT', 'SYN_RECV', 'FIN_WAIT', 'CLOSE_WAIT', 'CLOSE', 'LAST_ACK'];
        for (var i = 0; i < sOrder.length; i++) {
            if (states[sOrder[i]]) { sLabels.push(sOrder[i]); sValues.push(states[sOrder[i]]); sColors.push(natStateColors[sOrder[i]] || '#71717a'); }
        }
        for (var k in states) { if (sOrder.indexOf(k) === -1) { sLabels.push(k); sValues.push(states[k]); sColors.push('#71717a'); } }
        updateSimpleDoughnut(natStateChart, document.getElementById('natStateLegend'), sLabels, sValues, sColors);

        // NAT type doughnut
        var natTypes = ct.nat_types || {};
        var nLabels = [], nValues = [], nColors = [];
        var nOrder = ['snat', 'dnat', 'both', 'none'];
        var nDisplayNames = { 'snat': 'SNAT', 'dnat': 'DNAT', 'both': 'Both', 'none': 'No NAT' };
        for (var i = 0; i < nOrder.length; i++) {
            if (natTypes[nOrder[i]]) { nLabels.push(nDisplayNames[nOrder[i]]); nValues.push(natTypes[nOrder[i]]); nColors.push(natTypeColors[nOrder[i]]); }
        }
        updateSimpleDoughnut(natTypeChart, document.getElementById('natTypeLegend'), nLabels, nValues, nColors);

        // IP version doughnut
        var vLabels = [], vValues = [], vColors = [];
        if (ct.ipv4) { vLabels.push('IPv4'); vValues.push(ct.ipv4); vColors.push('#60a5fa'); }
        if (ct.ipv6) { vLabels.push('IPv6'); vValues.push(ct.ipv6); vColors.push('#a855f7'); }
        updateSimpleDoughnut(natIPvChart, document.getElementById('natIPvLegend'), vLabels, vValues, vColors);

        // Top LAN clients / remote destinations tables
        renderHostTable('natSrcTable', ct.top_lan_clients || []);
        renderHostTable('natDstTable', ct.top_remote_destinations || []);

        // Entry table
        renderNATEntries(ct);
    }

    function updateSimpleDoughnut(chart, legendEl, labels, values, colors, formatter) {
        chart.data.labels = labels;
        chart.data.datasets[0].data = values;
        chart.data.datasets[0].backgroundColor = colors;
        chart.update('none');
        var total = 0;
        for (var i = 0; i < values.length; i++) total += values[i];
        var h = '';
        for (var i = 0; i < labels.length; i++) {
            var pct = total > 0 ? ((values[i] / total) * 100).toFixed(1) : '0.0';
            var displayVal = formatter ? formatter(values[i]) : values[i].toLocaleString();
            h += '<div style="display:flex;align-items:center;gap:10px;margin-bottom:10px">';
            h += '<div style="width:10px;height:10px;border-radius:2px;background:' + colors[i] + ';flex-shrink:0"></div>';
            h += '<div><div style="font-size:13px;font-weight:600;color:var(--text-0)">' + labels[i] + '</div>';
            h += '<div style="font-size:11px;color:var(--text-2)">' + displayVal + ' &middot; ' + pct + '%</div></div></div>';
        }
        legendEl.innerHTML = h || '<div style="text-align:center;color:var(--text-2);font-size:12px;padding:8px">No data</div>';
    }

    var _natHostSortMode = { natSrc: 'bytes', natDst: 'bytes' };
    var _natHostData = { natSrc: [], natDst: [] };

    window._toggleHostSort = function(btn) {
        var target = btn.getAttribute('data-target');
        var mode = btn.getAttribute('data-mode');
        _natHostSortMode[target] = mode;
        var siblings = btn.parentElement.querySelectorAll('.toggle-btn');
        for (var i = 0; i < siblings.length; i++) siblings[i].classList.remove('active');
        btn.classList.add('active');
        var subtitle = mode === 'bytes' ? 'By traffic volume' : 'By connection count';
        var header = mode === 'bytes' ? 'Traffic' : 'Connections';
        document.getElementById(target + 'Subtitle').textContent = subtitle;
        document.getElementById(target + 'MetricHeader').textContent = header;
        renderHostTable(target + 'Table', _natHostData[target], mode);
    };

    function renderHostTable(tbId, hosts, mode) {
        if (!mode) mode = _natHostSortMode[tbId.replace('Table', '')] || 'bytes';
        var target = tbId.replace('Table', '');
        _natHostData[target] = hosts;
        var tb = document.getElementById(tbId);
        if (!hosts || !hosts.length) { tb.innerHTML = '<tr><td colspan="4" class="empty-state">No data</td></tr>'; return; }
        hosts = hosts.slice().sort(function(a, b) {
            if (mode === 'bytes') return (b.bytes || 0) - (a.bytes || 0);
            return (b.connections || 0) - (a.connections || 0);
        });
        var useBytes = mode === 'bytes';
        var mx = useBytes ? (hosts[0].bytes || 1) : (hosts[0].connections || 1);
        var h = '';
        for (var i = 0; i < hosts.length; i++) {
            var host = hosts[i];
            var val = useBytes ? (host.bytes || 0) : host.connections;
            var pct = ((val / mx) * 100).toFixed(1);
            var display = useBytes ? formatBytes(val) : val.toLocaleString();
            var flag = host.country ? countryFlag(host.country) + ' ' : '';
            var geo = '';
            var geoName = (host.city && host.country_name) ? host.city + ', ' + host.country_name : (host.country_name || '');
            if (host.as_org) geo = '<span class="hostname">' + flag + geoName + ' &middot; AS' + (host.asn || '') + ' ' + host.as_org + '</span>';
            else if (geoName) geo = '<span class="hostname">' + flag + geoName + '</span>';
            var cell = host.hostname
                ? '<span class="ip-cell ip-clickable" data-ip="' + host.ip + '">' + host.ip + '</span><span class="hostname">' + host.hostname + '</span>' + geo
                : '<span class="ip-cell ip-clickable" data-ip="' + host.ip + '">' + host.ip + '</span>' + geo;
            h += '<tr><td><span class="' + rankClass(i) + '">' + (i + 1) + '</span></td>';
            h += '<td>' + cell + '</td>';
            h += '<td style="font-variant-numeric:tabular-nums">' + display + '</td>';
            h += '<td class="bar-cell"><div class="bar-bg"></div><div class="bar-fill bw" style="width:' + pct + '%"></div></td></tr>';
        }
        tb.innerHTML = h;
    }

    function renderNATEntries(ct) {
        var entries = _currentNatVersion === 'v4' ? (ct.ipv4_entries || []) : (ct.ipv6_entries || []);
        var filter = ((document.getElementById('natFilter') || {}).value || 'all');
        var search = ((document.getElementById('natSearch') || {}).value || '').toLowerCase();

        if (filter !== 'all') {
            entries = entries.filter(function(e) { return e.nat_type === filter; });
        }
        if (search) {
            entries = entries.filter(function(e) {
                return (e.orig_src || '').indexOf(search) !== -1 ||
                       (e.orig_dst || '').indexOf(search) !== -1 ||
                       (e.repl_src || '').indexOf(search) !== -1 ||
                       (e.repl_dst || '').indexOf(search) !== -1 ||
                       (e.orig_src_host || '').toLowerCase().indexOf(search) !== -1 ||
                       (e.orig_dst_host || '').toLowerCase().indexOf(search) !== -1 ||
                       (e.orig_dst_asn || '').toLowerCase().indexOf(search) !== -1 ||
                       (e.orig_src_asn || '').toLowerCase().indexOf(search) !== -1 ||
                       (e.protocol || '').toLowerCase().indexOf(search) !== -1 ||
                       (e.state || '').toLowerCase().indexOf(search) !== -1 ||
                       (e.orig_sport || '').indexOf(search) !== -1 ||
                       (e.orig_dport || '').indexOf(search) !== -1;
            });
        }

        var tb = document.getElementById('natEntryTable');
        if (!entries.length) {
            tb.innerHTML = '<tr><td colspan="10" class="empty-state">' + (search || filter !== 'all' ? 'No matching entries' : 'No ' + (_currentNatVersion === 'v4' ? 'IPv4' : 'IPv6') + ' entries') + '</td></tr>';
            return;
        }

        var h = '';
        for (var i = 0; i < entries.length; i++) {
            var e = entries[i];
            var stateClass = 'other';
            if (e.state === 'ESTABLISHED') stateClass = 'established';
            else if (e.state === 'TIME_WAIT') stateClass = 'time-wait';
            else if (e.state === 'SYN_SENT' || e.state === 'SYN_RECV') stateClass = 'syn-sent';
            else if (e.state === 'CLOSE_WAIT' || e.state === 'CLOSE' || e.state === 'LAST_ACK') stateClass = 'close-wait';

            var origSrc = e.orig_src + (e.orig_sport ? ':' + e.orig_sport : '');
            var origDst = e.orig_dst + (e.orig_dport ? ':' + e.orig_dport : '');
            var replSrc = e.repl_src + (e.repl_sport ? ':' + e.repl_sport : '');
            var replDst = e.repl_dst + (e.repl_dport ? ':' + e.repl_dport : '');

            // Highlight translated addresses
            var replSrcHL = (e.repl_src !== e.orig_dst) ? ' style="color:var(--tx);font-weight:600"' : '';
            var replDstHL = (e.repl_dst !== e.orig_src) ? ' style="color:var(--rx);font-weight:600"' : '';

            // Helper: render IP cell with optional enrichment below
            function ipCell(addr, host, geo, city, asn, hlStyle) {
                var s = '<td' + (hlStyle || '') + '><div style="font-family:JetBrains Mono,monospace;font-size:11px;white-space:nowrap">' + addr + '</div>';
                var info = [];
                if (host) info.push(host);
                if (city && geo) info.push(countryFlag(geo) + ' ' + city + ', ' + geo);
                else if (geo) info.push(countryFlag(geo) + ' ' + geo);
                if (asn) info.push(asn);
                if (info.length) s += '<div style="font-size:9px;color:var(--text-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:220px">' + info.join(' · ') + '</div>';
                s += '</td>';
                return s;
            }

            h += '<tr>';
            h += '<td style="font-size:12px;font-weight:500">' + (e.protocol || '').toUpperCase() + '</td>';
            h += '<td>' + (e.state ? '<span class="nat-state-badge ' + stateClass + '">' + e.state + '</span>' : '<span style="color:var(--text-2)">—</span>') + '</td>';
            h += ipCell(origSrc, e.orig_src_host, e.orig_src_geo, e.orig_src_city, e.orig_src_asn, '');
            h += ipCell(origDst, e.orig_dst_host, e.orig_dst_geo, e.orig_dst_city, e.orig_dst_asn, '');
            h += '<td style="font-family:JetBrains Mono,monospace;font-size:11px;white-space:nowrap"' + replSrcHL + '>' + replSrc + '</td>';
            h += '<td style="font-family:JetBrains Mono,monospace;font-size:11px;white-space:nowrap"' + replDstHL + '>' + replDst + '</td>';
            h += '<td><span class="nat-badge ' + e.nat_type + '">' + (e.nat_type || 'none').toUpperCase() + '</span></td>';
            h += '<td style="font-variant-numeric:tabular-nums;font-size:11px;white-space:nowrap">' + (e.bytes ? formatBytes(e.bytes) : '<span style="color:var(--text-2)">—</span>') + '</td>';
            h += '<td style="font-variant-numeric:tabular-nums;font-size:11px;white-space:nowrap">' + (e.packets ? e.packets.toLocaleString() : '<span style="color:var(--text-2)">—</span>') + '</td>';
            h += '<td style="font-variant-numeric:tabular-nums;font-size:12px;color:var(--text-2)">' + e.ttl + 's</td>';
            h += '</tr>';
        }
        tb.innerHTML = h;
    }

    // Wire search/filter on NAT entry table to re-render (with debounce on search)
    var _natSearchTimer = null;
    (function() {
        var searchEl = document.getElementById('natSearch');
        if (searchEl) searchEl.addEventListener('input', function() {
            clearTimeout(_natSearchTimer);
            _natSearchTimer = setTimeout(function() {
                if (_lastConntrack) renderNATEntries(_lastConntrack);
            }, 150);
        });
        var filterEl = document.getElementById('natFilter');
        if (filterEl) filterEl.addEventListener('change', function() {
            if (_lastConntrack) renderNATEntries(_lastConntrack);
        });
    })();

    // ── World Traffic Map ──
    // Country centroids (ISO alpha-2 → [lat, lon]) for map visualization.
    var countryCentroids = {"AF":[33,65],"AL":[41,20],"DZ":[28,3],"AO":[-12,17],"AR":[-34,-64],"AM":[40,45],"AU":[-25,134],"AT":[47,14],"AZ":[41,48],"BD":[24,90],"BY":[53,28],"BE":[51,4],"BJ":[9,2],"BO":[-17,-65],"BA":[44,18],"BW":[-22,24],"BR":[-10,-55],"BG":[43,25],"BF":[12,-2],"KH":[13,105],"CM":[6,12],"CA":[56,-96],"CF":[7,21],"TD":[15,19],"CL":[-30,-71],"CN":[35,105],"CO":[4,-72],"CD":[-3,24],"CG":[-1,15],"CR":[10,-84],"CI":[8,-5],"HR":[45,16],"CU":[22,-80],"CY":[35,33],"CZ":[50,15],"DK":[56,10],"DO":[19,-70],"EC":[-2,-78],"EG":[27,30],"SV":[14,-89],"EE":[59,26],"ET":[9,40],"FI":[64,26],"FR":[46,2],"GA":[0,12],"DE":[51,9],"GH":[8,-2],"GR":[39,22],"GT":[16,-90],"GN":[11,-12],"HT":[19,-72],"HN":[15,-87],"HU":[47,20],"IS":[65,-18],"IN":[21,78],"ID":[-5,120],"IR":[32,53],"IQ":[33,44],"IE":[53,-8],"IL":[31,35],"IT":[43,12],"JM":[18,-77],"JP":[36,138],"JO":[31,37],"KZ":[48,68],"KE":[-1,38],"KW":[29,48],"KG":[41,75],"LA":[18,105],"LV":[57,25],"LB":[34,36],"LY":[27,17],"LT":[56,24],"LU":[50,6],"MG":[-19,47],"MY":[4,109],"ML":[17,-4],"MX":[23,-102],"MD":[47,29],"MN":[48,106],"ME":[43,19],"MA":[32,-5],"MZ":[-18,35],"MM":[22,96],"NA":[-22,17],"NP":[28,84],"NL":[52,5],"NZ":[-41,174],"NI":[13,-85],"NE":[18,8],"NG":[10,8],"KP":[40,127],"NO":[62,10],"OM":[21,57],"PK":[30,70],"PA":[9,-80],"PY":[-23,-58],"PE":[-10,-76],"PH":[13,122],"PL":[52,20],"PT":[39,-8],"QA":[25,51],"RO":[46,25],"RU":[62,105],"RW":[-2,30],"SA":[24,45],"SN":[14,-14],"RS":[44,21],"SG":[1,104],"SK":[49,20],"SI":[46,15],"ZA":[-29,24],"KR":[36,128],"ES":[40,-4],"LK":[8,81],"SD":[13,30],"SE":[62,16],"CH":[47,8],"SY":[35,38],"TW":[24,121],"TJ":[39,69],"TZ":[-7,35],"TH":[15,101],"TN":[34,9],"TR":[39,35],"TM":[39,60],"UA":[49,32],"AE":[24,54],"GB":[54,-2],"US":[38,-97],"UY":[-33,-56],"UZ":[41,65],"VE":[8,-66],"VN":[16,108],"YE":[16,48],"ZM":[-14,28],"ZW":[-19,30]};

    function updateWorldMap(countries, topBW, originCountry, originLat, originLon) {
        if (!countries || !countries.length) return;
        var wc = window._worldCountries || {};

        var container = document.getElementById('worldMapContainer');
        var W = container.clientWidth || 800;
        var H = Math.round(W * 0.5);

        function proj(lat, lon) {
            return [(lon + 180) / 360 * W, (90 - lat) / 180 * H];
        }

        // Build traffic lookup
        var trafficByCC = {};
        var maxBytes = 1;
        for (var i = 0; i < countries.length; i++) {
            var c = countries[i];
            trafficByCC[c.country] = c;
            if (c.bytes > maxBytes) maxBytes = c.bytes;
        }

        var isDark = (function() {
            var t = document.documentElement.getAttribute('data-theme');
            if (t === 'dark') return true;
            if (t === 'light') return false;
            return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
        })();

        var oceanBg = isDark ? '#0c1929' : '#eaeff6';
        var landDefault = isDark ? '#1c2e4a' : '#c5cdd9';
        var landStroke = isDark ? '#263d5e' : '#a8b2bf';
        var activeColor = '#22d3ee';
        var flowColor = '#a78bfa';
        var labelColor = isDark ? '#0c1929' : '#fff';

        var svg = '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;height:100%;border-radius:8px">';
        svg += '<defs><filter id="glow"><feGaussianBlur stdDeviation="2" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter></defs>';
        svg += '<rect width="' + W + '" height="' + H + '" fill="' + oceanBg + '" rx="8"/>';

        // Grid
        svg += '<g stroke="' + (isDark ? '#1a2a45' : '#d5dde8') + '" stroke-width="0.5" opacity="0.5">';
        for (var lon = -150; lon <= 180; lon += 30) { var gx = (lon + 180) / 360 * W; svg += '<line x1="' + gx + '" y1="0" x2="' + gx + '" y2="' + H + '"/>'; }
        for (var lat = -60; lat <= 80; lat += 30) { var gy = (90 - lat) / 180 * H; svg += '<line x1="0" y1="' + gy + '" x2="' + W + '" y2="' + gy + '"/>'; }
        svg += '</g>';

        // Country shapes from embedded GeoJSON
        var activeCCs = {};
        for (var cc in wc) {
            var polys = wc[cc];
            var traffic = trafficByCC[cc];
            var fill = landDefault, stroke = landStroke, sw = '0.3', fo = '1';
            if (traffic) {
                var ratio = traffic.bytes / maxBytes;
                fill = activeColor; fo = '' + (0.3 + ratio * 0.7).toFixed(2);
                stroke = activeColor; sw = '0.5';
                activeCCs[cc] = { ratio: ratio, traffic: traffic };
            }
            for (var pi = 0; pi < polys.length; pi++) {
                var ring = polys[pi];
                if (ring.length < 3) continue;
                // Handle antimeridian: split ring into segments that don't
                // cross the date line, so each segment renders correctly.
                var segments = [[]];
                segments[0].push(ring[0]);
                for (var ri = 1; ri < ring.length; ri++) {
                    if (Math.abs(ring[ri][0] - ring[ri-1][0]) > 170) {
                        // Date line crossing — start a new segment
                        segments.push([]);
                    }
                    segments[segments.length - 1].push(ring[ri]);
                }
                for (var si = 0; si < segments.length; si++) {
                    var seg = segments[si];
                    if (seg.length < 3) continue;
                    var d = '';
                    for (var ri = 0; ri < seg.length; ri++) {
                        var p = proj(seg[ri][1], seg[ri][0]);
                        d += (ri === 0 ? 'M' : 'L') + p[0].toFixed(0) + ',' + p[1].toFixed(0);
                    }
                    d += 'Z';
                    svg += '<path d="' + d + '" fill="' + fill + '" fill-opacity="' + fo + '" stroke="' + stroke + '" stroke-width="' + sw + '"';
                    if (traffic) svg += ' class="map-tip" data-tip="' + countryFlag(cc) + ' ' + (traffic.country_name || cc) + ': ' + formatBytes(traffic.bytes) + ' (' + traffic.connections + ' IPs)"';
                    svg += '/>';
                }
            }
        }

        // Flow lines from top bandwidth talkers
        if (topBW && topBW.length) {
            // Origin: use city-level coordinates if available, fall back to country centroid
            var oc = originCountry && countryCentroids[originCountry] ? originCountry : 'DE';
            var center = (originLat && originLon) ? proj(originLat, originLon) : proj(countryCentroids[oc][0], countryCentroids[oc][1]);
            svg += '<circle cx="' + center[0] + '" cy="' + center[1] + '" r="3" fill="' + flowColor + '" opacity="0.8"><animate attributeName="r" values="2;6;2" dur="2s" repeatCount="indefinite"/></circle>';

            // Flow colors: green for download (RX toward us), orange for upload (TX away)
            var rxColor = isDark ? '#34d399' : '#059669'; // emerald
            var txColor = isDark ? '#fb923c' : '#ea580c'; // orange

            // Find max rate across both directions for relative scaling
            var maxRate = 1;
            for (var i = 0; i < Math.min(topBW.length, 8); i++) {
                var rx = topBW[i].rx_rate || 0;
                var tx = topBW[i].tx_rate || 0;
                if (rx > maxRate) maxRate = rx;
                if (tx > maxRate) maxRate = tx;
            }
            // Track how many flows per country to fan out overlapping lines
            var ccFlowIdx = {};
            for (var i = 0; i < Math.min(topBW.length, 8); i++) {
                var t = topBW[i];
                if (!t.country || !countryCentroids[t.country]) continue;
                var cc = t.country;
                if (!ccFlowIdx[cc]) ccFlowIdx[cc] = 0;
                var fi = ccFlowIdx[cc]++;
                // Use city-level coordinates if available, fall back to country centroid
                var dest = (t.lat && t.lon) ? proj(t.lat, t.lon) : proj(countryCentroids[cc][0], countryCentroids[cc][1]);
                var curveOffset = 35 + fi * 18;
                var spreadX = (fi - 0.5) * 25;
                var mx = (center[0] + dest[0]) / 2 + spreadX;
                var my = Math.min(center[1], dest[1]) - curveOffset;
                var dur = (1.3 + fi * 0.2).toFixed(1);
                var host = t.hostname && t.hostname !== t.ip ? t.hostname + ' (' + t.ip + ')' : t.ip;
                var asInfo = t.as_org ? ' \u00b7 AS' + (t.asn || '') + ' ' + t.as_org : '';
                var rxRate = t.rx_rate || 0;
                var txRate = t.tx_rate || 0;

                // Compute perpendicular offset so RX/TX lines don't overlap.
                // One curves to the left of the direct path, the other to the right.
                var ldx = dest[0] - center[0];
                var ldy = dest[1] - center[1];
                var lineLen = Math.sqrt(ldx * ldx + ldy * ldy) || 1;
                // Unit perpendicular vector (rotated 90° CCW)
                var perpX = -ldy / lineLen;
                var perpY = ldx / lineLen;
                // Separation scales with line length, clamped to a reasonable range
                var sep = Math.min(40, Math.max(20, lineLen * 0.08));

                // RX line: remote → us (download, green) — curves one side
                if (rxRate > 0) {
                    var rxRatio = rxRate / maxRate;
                    var rxSw = (0.8 + rxRatio * 3.2).toFixed(1);
                    var rxOp = (0.25 + rxRatio * 0.55).toFixed(2);
                    var rxMx = mx + perpX * sep;
                    var rxMy = my + perpY * sep;
                    // Path: from remote to center (download direction)
                    var rxPath = 'M' + dest[0] + ',' + dest[1] + ' Q' + rxMx.toFixed(1) + ',' + rxMy.toFixed(1) + ' ' + center[0] + ',' + center[1];
                    var rxTip = host + asInfo + ' \u2192 \u2193 ' + formatRate(rxRate);
                    svg += '<path d="' + rxPath + '" fill="none" stroke="transparent" stroke-width="8" style="cursor:pointer" class="map-tip" data-tip="' + rxTip + '"/>';
                    svg += '<path d="' + rxPath + '" fill="none" stroke="' + rxColor + '" stroke-width="' + rxSw + '" stroke-dasharray="6,4" opacity="' + rxOp + '" stroke-linecap="round" style="pointer-events:none"><animate attributeName="stroke-dashoffset" from="0" to="-20" dur="' + dur + 's" repeatCount="indefinite"/></path>';
                }

                // TX line: us → remote (upload, orange) — curves opposite side
                if (txRate > 0) {
                    var txRatio = txRate / maxRate;
                    var txSw = (0.8 + txRatio * 3.2).toFixed(1);
                    var txOp = (0.25 + txRatio * 0.55).toFixed(2);
                    var txMx = mx - perpX * sep;
                    var txMy = my - perpY * sep;
                    // Path: from center to remote (upload direction)
                    var txPath = 'M' + center[0] + ',' + center[1] + ' Q' + txMx.toFixed(1) + ',' + txMy.toFixed(1) + ' ' + dest[0] + ',' + dest[1];
                    var txTip = host + asInfo + ' \u2192 \u2191 ' + formatRate(txRate);
                    svg += '<path d="' + txPath + '" fill="none" stroke="transparent" stroke-width="8" style="cursor:pointer" class="map-tip" data-tip="' + txTip + '"/>';
                    svg += '<path d="' + txPath + '" fill="none" stroke="' + txColor + '" stroke-width="' + txSw + '" stroke-dasharray="6,4" opacity="' + txOp + '" stroke-linecap="round" style="pointer-events:none"><animate attributeName="stroke-dashoffset" from="0" to="-20" dur="' + dur + 's" repeatCount="indefinite"/></path>';
                }

                // Destination dot — size by total rate, colored by dominant direction
                var totalRate = rxRate + txRate;
                var dotRatio = totalRate / (maxRate * 2 || 1);
                var dotR = (2 + dotRatio * 4).toFixed(1);
                var dotColor = rxRate >= txRate ? rxColor : txColor;
                var dotOp = (0.3 + dotRatio * 0.5).toFixed(2);
                svg += '<circle cx="' + dest[0] + '" cy="' + dest[1] + '" r="' + dotR + '" fill="' + dotColor + '" opacity="' + dotOp + '" style="pointer-events:none"/>';
            }
        }

        // Country code labels on active countries
        for (var cc in activeCCs) {
            if (!countryCentroids[cc]) continue;
            var p = proj(countryCentroids[cc][0], countryCentroids[cc][1]);
            var fs = Math.max(7, Math.min(12, 7 + activeCCs[cc].ratio * 8));
            svg += '<text x="' + p[0] + '" y="' + (p[1] + fs * 0.35) + '" text-anchor="middle" fill="' + labelColor + '" font-size="' + fs.toFixed(0) + 'px" font-weight="700" style="pointer-events:none;text-shadow:0 1px 3px rgba(0,0,0,0.6)">' + cc + '</text>';
        }
        svg += '</svg>';
        container.innerHTML = svg;

        // Custom tooltip for map elements (native <title> mispositions on transformed SVG)
        var tipEl = document.getElementById('mapTooltip');
        if (!tipEl) {
            tipEl = document.createElement('div');
            tipEl.id = 'mapTooltip';
            tipEl.style.cssText = 'position:fixed;pointer-events:none;background:var(--card);color:var(--text-0);border:1px solid var(--border);padding:6px 10px;border-radius:6px;font-size:12px;font-family:Inter,sans-serif;z-index:9999;display:none;white-space:nowrap;box-shadow:0 4px 12px rgba(0,0,0,0.15)';
            document.body.appendChild(tipEl);
        }
        container.addEventListener('mousemove', function(e) {
            var el = e.target.closest('.map-tip');
            if (el) {
                tipEl.textContent = el.getAttribute('data-tip');
                tipEl.style.display = '';
                tipEl.style.left = (e.clientX + 12) + 'px';
                tipEl.style.top = (e.clientY - 28) + 'px';
            } else {
                tipEl.style.display = 'none';
            }
        });
        container.addEventListener('mouseleave', function() {
            tipEl.style.display = 'none';
        });

        // Apply zoom/pan transform (preserve across updates)
        var svgEl = container.querySelector('svg');
        if (svgEl && window._mapZoom) {
            svgEl.style.transformOrigin = '0 0';
            svgEl.style.transform = 'scale(' + window._mapZoom.scale + ') translate(' + window._mapZoom.tx + 'px,' + window._mapZoom.ty + 'px)';
        }

        // Country traffic table
        var tableWrap = document.getElementById('mapCountryTable');
        var tableEl = document.getElementById('mapCountryList');
        if (tableWrap && tableEl) {
            tableWrap.style.display = '';
            var total = 0;
            for (var i = 0; i < countries.length; i++) total += countries[i].bytes;
            var th = '';
            for (var i = 0; i < countries.length; i++) {
                var c = countries[i];
                var pct = total > 0 ? (c.bytes / total * 100) : 0;
                th += '<div style="display:flex;align-items:center;gap:6px;padding:3px 6px;border-radius:4px;background:var(--bg-1)">';
                th += '<span style="width:20px;text-align:center">' + countryFlag(c.country) + '</span>';
                th += '<span style="font-weight:600;width:24px">' + c.country + '</span>';
                th += '<div style="flex:1;height:6px;background:var(--bg-2);border-radius:3px;overflow:hidden"><div style="width:' + Math.max(2, pct).toFixed(1) + '%;height:100%;background:' + activeColor + ';border-radius:3px;opacity:0.7"></div></div>';
                th += '<span style="font-variant-numeric:tabular-nums;color:var(--text-2);min-width:55px;text-align:right">' + formatBytes(c.bytes) + '</span>';
                th += '<span style="color:var(--text-3);min-width:38px;text-align:right">' + pct.toFixed(1) + '%</span>';
                th += '</div>';
            }
            tableEl.innerHTML = th;
        }
    }

    // ── Map zoom/pan ──
    (function initMapZoom() {
        // Default: zoomed to show mostly Northern Hemisphere (Europe/US focus)
        window._mapZoom = { scale: 1.6, tx: -120, ty: -40 };
        var mc = document.getElementById('worldMapContainer');
        if (!mc) return;

        var dragging = false, lastX = 0, lastY = 0;

        mc.addEventListener('wheel', function(e) {
            // Require Ctrl/Cmd to zoom — otherwise let the page scroll normally
            if (!e.ctrlKey && !e.metaKey) return;
            e.preventDefault();
            var z = window._mapZoom;
            var delta = e.deltaY > 0 ? 0.97 : 1.03;
            var newScale = Math.max(1, Math.min(6, z.scale * delta));
            var rect = mc.getBoundingClientRect();
            var mx = e.clientX - rect.left;
            var my = e.clientY - rect.top;
            z.tx = mx - (mx - z.tx) * (newScale / z.scale);
            z.ty = my - (my - z.ty) * (newScale / z.scale);
            z.scale = newScale;
            applyMapTransform(mc);
        }, { passive: false });

        mc.addEventListener('mousedown', function(e) {
            if (e.button !== 0) return;
            dragging = true;
            lastX = e.clientX;
            lastY = e.clientY;
            mc.style.cursor = 'grabbing';
            e.preventDefault();
        });
        window.addEventListener('mousemove', function(e) {
            if (!dragging) return;
            var z = window._mapZoom;
            z.tx += (e.clientX - lastX) / z.scale;
            z.ty += (e.clientY - lastY) / z.scale;
            lastX = e.clientX;
            lastY = e.clientY;
            applyMapTransform(mc);
        });
        window.addEventListener('mouseup', function() {
            if (dragging) {
                dragging = false;
                mc.style.cursor = 'grab';
            }
        });

        // Double-click to reset
        mc.addEventListener('dblclick', function(e) {
            e.preventDefault();
            window._mapZoom = { scale: 1.6, tx: -120, ty: -40 };
            applyMapTransform(mc);
        });
    })();

    function applyMapTransform(container) {
        var svgEl = container.querySelector('svg');
        if (!svgEl) return;
        var z = window._mapZoom;
        svgEl.style.transformOrigin = '0 0';
        svgEl.style.transform = 'scale(' + z.scale + ') translate(' + z.tx + 'px,' + z.ty + 'px)';
    }

    // ── Latency Monitor ──
    function updateLatency(targets) {
        var sec = document.getElementById('latencySection');
        if (!targets || !targets.length) return;

        var el = document.getElementById('latencyTargets');
        el.style.display = 'grid';
        el.style.gridTemplateColumns = 'repeat(auto-fill,minmax(380px,1fr))';
        el.style.gap = '16px';

        var h = '';
        for (var i = 0; i < targets.length; i++) {
            var t = targets[i];
            var statusColor = t.alive ? 'var(--success, #22c55e)' : 'var(--danger, #ef4444)';
            var statusText = t.alive ? 'UP' : 'DOWN';

            // Determine if dual-stack
            var hasV4 = t.ipv4 && t.ipv4 !== '';
            var hasV6 = t.ipv6 && t.ipv6 !== '';
            var dualStack = hasV4 && hasV6;

            h += '<div style="background:var(--bg-2);border-radius:8px;padding:16px;border:1px solid var(--border)">';

            // Header: name + IPs + status
            h += '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">';
            h += '<div>';
            h += '<div style="font-weight:600;font-size:14px">' + t.name + '</div>';
            h += '<div style="font-size:10px;color:var(--text-2);font-family:var(--font-mono, monospace)">';
            if (hasV4) h += '<span style="background:var(--bg-1);padding:1px 4px;border-radius:3px;margin-right:4px">v4 ' + t.ipv4 + '</span>';
            if (hasV6) h += '<span style="background:var(--bg-1);padding:1px 4px;border-radius:3px">v6 ' + t.ipv6 + '</span>';
            // Legacy fallback for old data
            if (!hasV4 && !hasV6 && t.ip) h += t.ip;
            h += '</div>';
            h += '</div>';
            h += '<div style="display:flex;align-items:center;gap:6px">';
            if (dualStack) h += '<span style="font-size:9px;font-weight:600;color:var(--text-2);background:var(--bg-1);padding:1px 5px;border-radius:3px">DUAL</span>';
            h += '<span style="width:10px;height:10px;border-radius:50%;background:' + statusColor + ';display:inline-block"></span>';
            h += '<span style="font-size:12px;font-weight:700;color:' + statusColor + '">' + statusText + '</span>';
            h += '</div></div>';

            // Stats grid — per protocol
            function statsRow(label, labelColor, s) {
                if (!s) return '';
                var r = '<div style="display:grid;grid-template-columns:auto repeat(6,1fr);gap:8px;text-align:center;align-items:center">';
                r += '<div style="text-align:left;font-size:11px;font-weight:700;color:' + labelColor + '">' + label + '</div>';
                var items = [
                    ['RTT', s.rtt_ms >= 0 ? s.rtt_ms.toFixed(1) : '—', 'ms'],
                    ['Avg', s.avg_rtt_ms > 0 ? s.avg_rtt_ms.toFixed(1) : '—', 'ms'],
                    ['P95', s.p95_rtt_ms > 0 ? s.p95_rtt_ms.toFixed(1) : '—', 'ms'],
                    ['P99', s.p99_rtt_ms > 0 ? s.p99_rtt_ms.toFixed(1) : '—', 'ms'],
                    ['Jitter', s.jitter_ms > 0 ? s.jitter_ms.toFixed(2) : '—', 'ms'],
                    ['Loss', s.loss_pct.toFixed(1), '%']
                ];
                for (var si = 0; si < items.length; si++) {
                    var lossStyle = items[si][0] === 'Loss' && s.loss_pct > 0 ? ';color:var(--danger)' : '';
                    r += '<div><div style="font-size:9px;color:var(--text-2);margin-bottom:1px">' + items[si][0] + '</div>';
                    r += '<div style="font-size:12px;font-weight:600;font-variant-numeric:tabular-nums' + lossStyle + '">';
                    r += items[si][1] + '<span style="font-size:8px;color:var(--text-2);margin-left:1px">' + items[si][2] + '</span></div></div>';
                }
                r += '</div>';
                return r;
            }

            h += '<div style="margin-bottom:14px;display:flex;flex-direction:column;gap:6px">';
            h += statsRow('ICMP', '#22d3ee', t.icmp_stats);
            h += statsRow('HTTPS', '#a78bfa', t.https_stats);
            h += '</div>';

            // ICMP charts — show v4 and v6 separately if dual-stack
            var icmpV4 = t.icmp_v4 && t.icmp_v4.length > 1 ? t.icmp_v4 : null;
            var icmpV6 = t.icmp_v6 && t.icmp_v6.length > 1 ? t.icmp_v6 : null;
            var icmpLegacy = t.icmp && t.icmp.length > 1 ? t.icmp : null;

            // Helper: render a latency chart section with label
            function latencySection(label, v4Data, v6Data, legacyData, v4Color, v6Color, marginBottom) {
                var mb = marginBottom ? 'margin-bottom:10px' : '';
                var out = '';
                if (v4Data && v6Data) {
                    out += '<div style="margin-bottom:10px">';
                    out += '<div style="font-size:11px;font-weight:600;color:var(--text-2);margin-bottom:4px">' + label + ' <span style="color:' + v4Color + '">IPv4</span></div>';
                    out += renderLatencyChart(v4Data, v4Color, v4Color);
                    out += '</div>';
                    out += '<div style="' + mb + '">';
                    out += '<div style="font-size:11px;font-weight:600;color:var(--text-2);margin-bottom:4px">' + label + ' <span style="color:' + v6Color + '">IPv6</span></div>';
                    out += renderLatencyChart(v6Data, v6Color, v6Color);
                    out += '</div>';
                } else if (v4Data) {
                    out += '<div style="' + mb + '">';
                    out += '<div style="font-size:11px;font-weight:600;color:var(--text-2);margin-bottom:4px">' + label + ' (IPv4)</div>';
                    out += renderLatencyChart(v4Data, v4Color, v4Color);
                    out += '</div>';
                } else if (v6Data) {
                    out += '<div style="' + mb + '">';
                    out += '<div style="font-size:11px;font-weight:600;color:var(--text-2);margin-bottom:4px">' + label + ' (IPv6)</div>';
                    out += renderLatencyChart(v6Data, v6Color, v6Color);
                    out += '</div>';
                } else if (legacyData) {
                    out += '<div style="' + mb + '">';
                    out += '<div style="font-size:11px;font-weight:600;color:var(--text-2);margin-bottom:4px">' + label + '</div>';
                    out += renderLatencyChart(legacyData, v4Color, v4Color);
                    out += '</div>';
                }
                return out;
            }

            h += latencySection('ICMP Ping', icmpV4, icmpV6, icmpLegacy, '#22d3ee', '#34d399', true);

            // HTTPS charts — show v4 and v6 separately if dual-stack
            var httpsV4 = t.https_v4 && t.https_v4.length > 1 ? t.https_v4 : null;
            var httpsV6 = t.https_v6 && t.https_v6.length > 1 ? t.https_v6 : null;
            var httpsLegacy = t.https && t.https.length > 1 ? t.https : null;

            h += latencySection('HTTPS', httpsV4, httpsV6, httpsLegacy, '#a78bfa', '#fb923c', false);

            h += '</div>';
        }
        el.innerHTML = h;
    }

    function renderLatencyChart(points, strokeColor, fillHex) {
        var ML = 42; // left margin for Y axis labels
        var W = 360, H = 72;
        var chartW = W - ML;
        var PT = 4, PB = 12; // padding top/bottom inside chart

        // Find max RTT for scale
        var maxRTT = 1;
        for (var i = 0; i < points.length; i++) {
            if (points[i].rtt > maxRTT) maxRTT = points[i].rtt;
        }
        maxRTT = Math.max(maxRTT * 1.2, 5); // 20% headroom, min 5ms

        // Nice Y-axis ticks
        var yTicks = niceScale(0, maxRTT, 3);

        function yPx(val) { return PT + (1 - val / maxRTT) * (H - PT - PB); }

        var svg = '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;height:' + H + 'px;display:block">';

        // Chart background
        svg += '<rect x="' + ML + '" y="0" width="' + chartW + '" height="' + H + '" fill="var(--bg-1)" rx="4"/>';

        // Y-axis grid lines and labels
        for (var yi = 0; yi < yTicks.length; yi++) {
            var yVal = yTicks[yi];
            var y = yPx(yVal);
            if (y < PT || y > H - PB) continue;
            svg += '<line x1="' + ML + '" y1="' + y.toFixed(1) + '" x2="' + W + '" y2="' + y.toFixed(1) + '" stroke="var(--text-3)" stroke-width="0.5" opacity="0.25"/>';
            var label = yVal < 10 ? yVal.toFixed(1) : Math.round(yVal);
            svg += '<text x="' + (ML - 3) + '" y="' + (y + 3) + '" text-anchor="end" fill="var(--text-2)" font-size="9px" style="font-variant-numeric:tabular-nums">' + label + '</text>';
        }
        // "ms" unit label — positioned at top-left, clear of tick values
        svg += '<text x="2" y="9" fill="var(--text-3)" font-size="8px" font-weight="600">ms</text>';

        // Build path + fill using smooth cubic bezier curves
        var pathPts = [];
        var lossDots = '';
        for (var i = 0; i < points.length; i++) {
            var x = ML + (i / Math.max(points.length - 1, 1)) * chartW;
            if (points[i].rtt < 0) {
                lossDots += '<line x1="' + x.toFixed(1) + '" y1="' + PT + '" x2="' + x.toFixed(1) + '" y2="' + (H - PB) + '" stroke="var(--danger, #ef4444)" stroke-width="1" opacity="0.12"/>';
                continue;
            }
            pathPts.push({ x: x, y: yPx(points[i].rtt) });
        }
        var path = '', fillPath = '';
        if (pathPts.length > 1) {
            path = 'M' + pathPts[0].x.toFixed(1) + ',' + pathPts[0].y.toFixed(1);
            for (var pi = 1; pi < pathPts.length; pi++) {
                var cx = (pathPts[pi-1].x + pathPts[pi].x) / 2;
                path += ' C' + cx.toFixed(1) + ',' + pathPts[pi-1].y.toFixed(1) + ' ' + cx.toFixed(1) + ',' + pathPts[pi].y.toFixed(1) + ' ' + pathPts[pi].x.toFixed(1) + ',' + pathPts[pi].y.toFixed(1);
            }
            fillPath = 'M' + pathPts[0].x.toFixed(1) + ',' + (H - PB);
            fillPath += ' L' + pathPts[0].x.toFixed(1) + ',' + pathPts[0].y.toFixed(1);
            for (var pi = 1; pi < pathPts.length; pi++) {
                var cx = (pathPts[pi-1].x + pathPts[pi].x) / 2;
                fillPath += ' C' + cx.toFixed(1) + ',' + pathPts[pi-1].y.toFixed(1) + ' ' + cx.toFixed(1) + ',' + pathPts[pi].y.toFixed(1) + ' ' + pathPts[pi].x.toFixed(1) + ',' + pathPts[pi].y.toFixed(1);
            }
            fillPath += ' L' + pathPts[pathPts.length-1].x.toFixed(1) + ',' + (H - PB) + ' Z';
        } else if (pathPts.length === 1) {
            path = 'M' + pathPts[0].x.toFixed(1) + ',' + pathPts[0].y.toFixed(1);
        }

        // Loss strips
        svg += lossDots;

        // Fill area
        if (fillPath) {
            svg += '<path d="' + fillPath + '" fill="' + fillHex + '" opacity="0.1"/>';
        }
        // Line
        if (path) {
            svg += '<path d="' + path + '" fill="none" stroke="' + strokeColor + '" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/>';
        }

        // Time labels
        svg += '<text x="' + (ML + 3) + '" y="' + (H - 1) + '" fill="var(--text-3)" font-size="8px">15m ago</text>';
        svg += '<text x="' + (W - 3) + '" y="' + (H - 1) + '" text-anchor="end" fill="var(--text-3)" font-size="8px">now</text>';

        svg += '</svg>';
        return svg;
    }

    // Generate nice Y-axis tick values
    function niceScale(lo, hi, maxTicks) {
        var range = hi - lo;
        if (range <= 0) return [0];
        var rough = range / maxTicks;
        var mag = Math.pow(10, Math.floor(Math.log10(rough)));
        var res = rough / mag;
        var nice;
        if (res <= 1.5) nice = 1;
        else if (res <= 3) nice = 2;
        else if (res <= 7) nice = 5;
        else nice = 10;
        var step = nice * mag;
        var ticks = [];
        for (var v = 0; v <= hi; v += step) {
            ticks.push(Math.round(v * 1000) / 1000);
        }
        return ticks;
    }

    // ── WiFi ──
    function updateWiFi(wifi) {
        if (!wifi) return;
        document.getElementById('wifiNoData').style.display = 'none';
        document.getElementById('wifiHasData').style.display = '';
        var pn = document.getElementById('wifiProviderName');
        if (pn && wifi.provider_name) pn.textContent = wifi.provider_name;

        document.getElementById('wifiTotalAPs').textContent = wifi.total_aps || 0;
        document.getElementById('wifiTotalClients').textContent = wifi.total_clients || 0;
        document.getElementById('wifiTotalSSIDs').textContent = (wifi.ssids ? wifi.ssids.length : 0);

        // AP cards
        var apEl = document.getElementById('wifiAPCards');
        if (wifi.aps && wifi.aps.length) {
            var h = '';
            for (var i = 0; i < wifi.aps.length; i++) {
                var ap = wifi.aps[i];
                var st = ap.status === 'connected' ? 'up' : (ap.status === 'disconnected' ? 'down' : 'unknown');
                var stLabel = ap.status === 'connected' ? 'Online' : (ap.status === 'disconnected' ? 'Offline' : (ap.status || 'Unknown'));
                h += '<div class="wifi-ap-card">';
                h += '<div class="wifi-ap-name"><span>' + (ap.name || ap.mac || 'AP') + ' <span class="wifi-ap-model">' + (ap.model || '') + '</span></span>';
                h += '<span class="iface-status"><span class="iface-status-dot ' + st + '"></span>' + stLabel + '</span></div>';
                h += '<div class="wifi-ap-stats">';
                h += '<div><div class="wifi-ap-stat-label">Clients</div><div class="wifi-ap-stat-value" style="color:var(--rx)">' + (ap.num_clients || 0) + '</div></div>';
                h += '<div><div class="wifi-ap-stat-label">Firmware</div><div class="wifi-ap-stat-value" style="font-size:11px">' + (ap.version || '—') + '</div></div>';
                h += '<div><div class="wifi-ap-stat-label label-rx">RX Rate</div><div class="wifi-ap-stat-value" style="font-size:11px;color:var(--rx)">' + formatRate(ap.rx_rate || 0) + '</div></div>';
                h += '<div><div class="wifi-ap-stat-label label-tx">TX Rate</div><div class="wifi-ap-stat-value" style="font-size:11px;color:var(--tx)">' + formatRate(ap.tx_rate || 0) + '</div></div>';
                h += '<div><div class="wifi-ap-stat-label">RX Total</div><div class="wifi-ap-stat-value" style="font-size:11px">' + formatBytes(ap.rx_bytes || 0) + '</div></div>';
                h += '<div><div class="wifi-ap-stat-label">TX Total</div><div class="wifi-ap-stat-value" style="font-size:11px">' + formatBytes(ap.tx_bytes || 0) + '</div></div>';
                if (ap.ip) {
                    h += '<div><div class="wifi-ap-stat-label">IP</div><div class="wifi-ap-stat-value" style="font-size:11px;font-family:JetBrains Mono,monospace">' + ap.ip + '</div></div>';
                }
                if (ap.mac) {
                    h += '<div><div class="wifi-ap-stat-label">MAC</div><div class="wifi-ap-stat-value" style="font-size:11px;font-family:JetBrains Mono,monospace">' + ap.mac + '</div></div>';
                }
                if (ap.uptime) {
                    var days = Math.floor(ap.uptime / 86400);
                    var hrs = Math.floor((ap.uptime % 86400) / 3600);
                    h += '<div><div class="wifi-ap-stat-label">Uptime</div><div class="wifi-ap-stat-value" style="font-size:11px">' + days + 'd ' + hrs + 'h</div></div>';
                }
                h += '</div></div>';
            }
            apEl.innerHTML = h;
        } else {
            apEl.innerHTML = '<div class="empty-state">No access points found</div>';
        }

        // ── Clients per AP chart ──
        var apsByClients = (wifi.aps || []).slice().sort(function(a, b) { return (b.num_clients || 0) - (a.num_clients || 0); });
        fillDoughnut(apClientsChart, document.getElementById('apClientsLegend'),
            apsByClients,
            function(a) { return a.name || a.mac || 'AP'; },
            function(a) { return a.num_clients || 0; },
            function(v) { return v + ' clients'; }
        );
        fillDetailTable('apClientsTable', apsByClients,
            function(a) { return a.name || a.mac || 'AP'; },
            function(a) { return a.num_clients || 0; },
            function(v) { return v.toLocaleString(); }, 'bw'
        );

        // ── Traffic per AP chart ──
        var apsByTraffic = (wifi.aps || []).slice().sort(function(a, b) {
            return ((b.tx_bytes || 0) + (b.rx_bytes || 0)) - ((a.tx_bytes || 0) + (a.rx_bytes || 0));
        });
        fillDoughnut(apTrafficChart, document.getElementById('apTrafficLegend'),
            apsByTraffic,
            function(a) { return a.name || a.mac || 'AP'; },
            function(a) { return (a.tx_bytes || 0) + (a.rx_bytes || 0); },
            formatBytes
        );
        fillTrafficDetailTable('apTrafficTable', apsByTraffic, function(a) { return a.name || a.mac || 'AP'; });

        // ── Clients per SSID chart ──
        fillDoughnut(ssidChart, document.getElementById('ssidLegend'),
            wifi.ssids || [],
            function(s) { return s.name || '(hidden)'; },
            function(s) { return s.num_clients || 0; },
            function(v) { return v + ' clients'; }
        );
        fillDetailTable('wifiSSIDTable', wifi.ssids || [],
            function(s) { return s.name || '—'; },
            function(s) { return s.num_clients || 0; },
            function(v) { return v.toLocaleString(); }, 'bw'
        );

        // ── Traffic per SSID chart ──
        var ssidsByTraffic = (wifi.ssids || []).slice().sort(function(a, b) {
            return ((b.tx_bytes || 0) + (b.rx_bytes || 0)) - ((a.tx_bytes || 0) + (a.rx_bytes || 0));
        });
        fillDoughnut(ssidTrafficChart, document.getElementById('ssidTrafficLegend'),
            ssidsByTraffic,
            function(s) { return s.name || '(hidden)'; },
            function(s) { return (s.tx_bytes || 0) + (s.rx_bytes || 0); },
            formatBytes
        );
        fillTrafficDetailTable('ssidTrafficTable', ssidsByTraffic, function(s) { return s.name || '—'; });

        // ── Client traffic table ──
        var clients = (wifi.clients || []).slice();
        var sortKey = (document.getElementById('wifiClientSort') || {}).value || 'traffic';
        if (sortKey === 'name') {
            clients.sort(function(a, b) { return (a.hostname || a.mac || '').localeCompare(b.hostname || b.mac || ''); });
        } else if (sortKey === 'signal') {
            clients.sort(function(a, b) { return (a.signal || -100) - (b.signal || -100); });
        } else if (sortKey === 'rate') {
            clients.sort(function(a, b) {
                return ((b.rx_rate || 0) + (b.tx_rate || 0)) - ((a.rx_rate || 0) + (a.tx_rate || 0));
            });
        } // default: already sorted by traffic descending from backend

        var filter = ((document.getElementById('wifiClientSearch') || {}).value || '').toLowerCase();
        if (filter) {
            clients = clients.filter(function(cl) {
                return (cl.hostname || '').toLowerCase().indexOf(filter) !== -1 ||
                       (cl.ip || '').indexOf(filter) !== -1 ||
                       (cl.mac || '').toLowerCase().indexOf(filter) !== -1 ||
                       (cl.ssid || '').toLowerCase().indexOf(filter) !== -1 ||
                       (cl.ap_name || '').toLowerCase().indexOf(filter) !== -1;
            });
        }

        var ctb = document.getElementById('wifiClientTable');
        if (!clients.length) {
            ctb.innerHTML = '<tr><td colspan="11" class="empty-state">' + (filter ? 'No matching clients' : 'No wireless clients') + '</td></tr>';
        } else {
            var maxBw = 1;
            for (var i = 0; i < clients.length; i++) {
                var t = (clients[i].tx_bytes || 0) + (clients[i].rx_bytes || 0);
                if (t > maxBw) maxBw = t;
            }
            var ch = '';
            for (var i = 0; i < clients.length; i++) {
                var cl = clients[i];
                var total = (cl.tx_bytes || 0) + (cl.rx_bytes || 0);
                var pct = maxBw > 0 ? ((total / maxBw) * 100).toFixed(1) : '0';
                var name = cl.hostname || cl.ip || cl.mac || '—';
                var sub = cl.ip && cl.hostname ? cl.ip : (cl.mac || '');
                var sig = cl.signal || 0;
                var sigClass = sig >= -50 ? 'sig-great' : sig >= -65 ? 'sig-good' : sig >= -75 ? 'sig-ok' : 'sig-weak';
                ch += '<tr>';
                ch += '<td><span class="' + rankClass(i) + '">' + (i + 1) + '</span></td>';
                ch += '<td><span class="ip-cell">' + name + '</span>';
                if (sub && sub !== name) ch += '<div style="font-size:10px;color:var(--text-2);font-family:JetBrains Mono,monospace">' + sub + '</div>';
                ch += '</td>';
                ch += '<td style="font-size:12px">' + (cl.ssid || '—') + '</td>';
                ch += '<td style="font-size:12px">' + (cl.ap_name || '—') + '</td>';
                ch += '<td style="font-size:11px;color:var(--text-2)">' + (cl.radio || '—') + '</td>';
                ch += '<td style="font-size:11px;font-variant-numeric:tabular-nums">' + (cl.channel || '—') + '</td>';
                ch += '<td><span class="signal-badge ' + sigClass + '">' + sig + ' dBm</span></td>';
                ch += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums">' + formatBytes(cl.rx_bytes || 0) + '</td>';
                ch += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums">' + formatBytes(cl.tx_bytes || 0) + '</td>';
                var clRate = (cl.rx_rate || 0) + (cl.tx_rate || 0);
                ch += '<td style="white-space:nowrap;font-variant-numeric:tabular-nums;font-size:11px">';
                ch += '<span style="color:var(--rx)">' + formatRate(cl.rx_rate || 0) + '</span>';
                ch += ' <span style="color:var(--text-2)">/</span> ';
                ch += '<span style="color:var(--tx)">' + formatRate(cl.tx_rate || 0) + '</span></td>';
                ch += '<td class="bar-cell"><div class="bar-bg"></div><div class="bar-fill bw" style="width:' + pct + '%"></div></td>';
                ch += '</tr>';
            }
            ctb.innerHTML = ch;
        }
    }

    // ── Host Detail Modal ──
    window._openHostModal = function(ip) {
        var modal = document.getElementById('hostModal');
        var body = document.getElementById('hostModalBody');
        var title = document.getElementById('hostModalTitle');
        var subtitle = document.getElementById('hostModalSubtitle');
        modal.style.display = '';
        title.textContent = ip;
        subtitle.textContent = 'Loading…';
        body.innerHTML = '<div style="text-align:center;padding:32px;color:var(--text-2)">Loading…</div>';
        document.body.style.overflow = 'hidden';

        fetch('/api/host?ip=' + encodeURIComponent(ip))
            .then(function(r) { return r.json(); })
            .then(function(d) { renderHostModal(d, title, subtitle, body); })
            .catch(function(e) { body.innerHTML = '<div style="text-align:center;padding:32px;color:var(--danger)">Failed to load: ' + e + '</div>'; });
    };

    window._closeHostModal = function() {
        document.getElementById('hostModal').style.display = 'none';
        document.body.style.overflow = '';
    };

    // Close on Escape key
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape' && document.getElementById('hostModal').style.display !== 'none') {
            window._closeHostModal();
        }
    });

    function renderHostModal(d, titleEl, subtitleEl, bodyEl) {
        var flag = d.country ? countryFlag(d.country) + ' ' : '';
        titleEl.textContent = flag + (d.hostname || d.ip);
        var sub = d.ip;
        if (d.city && d.country_name) sub += ' \u00b7 ' + d.city + ', ' + d.country_name;
        else if (d.country_name) sub += ' \u00b7 ' + d.country_name;
        if (d.as_org) sub += ' \u00b7 AS' + (d.asn || '') + ' ' + d.as_org;
        subtitleEl.textContent = sub;

        var h = '';

        // Stats grid
        h += '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(110px,1fr));gap:16px;padding:4px 0 20px;border-bottom:1px solid var(--border);margin-bottom:20px">';
        function stat(label, value, color) {
            return '<div><div style="font-size:11px;color:var(--text-2);margin-bottom:4px">' + label + '</div><div style="font-size:18px;font-weight:700;font-variant-numeric:tabular-nums' + (color ? ';color:var(--' + color + ')' : '') + '">' + value + '</div></div>';
        }
        h += stat('Total', formatBytes(d.total_bytes));
        h += stat('RX', formatBytes(d.rx_bytes), 'rx');
        h += stat('TX', formatBytes(d.tx_bytes), 'tx');
        h += stat('Rate', formatRate(d.rate_bytes));
        h += stat('RX Rate', formatRate(d.rx_rate), 'rx');
        h += stat('TX Rate', formatRate(d.tx_rate), 'tx');
        h += stat('Packets', (d.packets || 0).toLocaleString());
        h += stat('Connections', d.connections ? d.connections.length.toLocaleString() : '0');
        h += '</div>';

        // Bandwidth history chart (SVG)
        if (d.history && d.history.length > 1) {
            h += '<div style="margin-bottom:20px">';
            h += '<div style="font-size:14px;font-weight:600;margin-bottom:10px">Traffic Volume <span style="font-weight:400;color:var(--text-2);font-size:12px">(24h, per minute)</span></div>';
            h += renderHostHistoryChart(d.history);
            h += '</div>';
        }

        // Connection table
        if (d.connections && d.connections.length > 0) {
            h += '<div style="font-size:14px;font-weight:600;margin-bottom:10px">Active Connections <span style="font-weight:400;color:var(--text-2);font-size:12px">(' + d.connections.length + ')</span></div>';
            h += '<div style="overflow-x:auto;max-height:350px;overflow-y:auto;border:1px solid var(--border);border-radius:8px">';
            h += '<table style="width:100%;font-size:12px;border-collapse:collapse"><thead><tr style="background:var(--bg-2);position:sticky;top:0">';
            h += '<th style="padding:8px 10px;text-align:left;font-size:11px;font-weight:600;color:var(--text-2)">Proto</th>';
            h += '<th style="padding:8px 10px;text-align:left;font-size:11px;font-weight:600;color:var(--text-2)">State</th>';
            h += '<th style="padding:8px 10px;text-align:left;font-size:11px;font-weight:600;color:var(--text-2)">Source</th>';
            h += '<th style="padding:8px 10px;text-align:left;font-size:11px;font-weight:600;color:var(--text-2)">Destination</th>';
            h += '<th style="padding:8px 10px;text-align:left;font-size:11px;font-weight:600;color:var(--text-2)">NAT</th>';
            h += '<th style="padding:8px 10px;text-align:right;font-size:11px;font-weight:600;color:var(--text-2)">Bytes</th>';
            h += '<th style="padding:8px 10px;text-align:right;font-size:11px;font-weight:600;color:var(--text-2)">Pkts</th>';
            h += '</tr></thead><tbody>';
            for (var i = 0; i < d.connections.length; i++) {
                var c = d.connections[i];
                var rowBg = i % 2 === 0 ? '' : ' style="background:var(--bg-1)"';
                // Source cell with enrichment
                var srcAddr = c.orig_src + (c.orig_sport ? ':' + c.orig_sport : '');
                var srcInfo = [];
                if (c.orig_src_host) srcInfo.push(c.orig_src_host);
                if (c.orig_src_city && c.orig_src_geo) srcInfo.push(countryFlag(c.orig_src_geo) + ' ' + c.orig_src_city);
                else if (c.orig_src_geo) srcInfo.push(countryFlag(c.orig_src_geo) + ' ' + c.orig_src_geo);
                if (c.orig_src_asn) srcInfo.push(c.orig_src_asn);
                var srcHtml = '<div style="font-family:var(--font-mono,monospace);font-size:11px">' + srcAddr + '</div>';
                if (srcInfo.length) srcHtml += '<div style="font-size:9px;color:var(--text-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:200px">' + srcInfo.join(' &middot; ') + '</div>';
                // Dest cell with enrichment
                var dstAddr = c.orig_dst + (c.orig_dport ? ':' + c.orig_dport : '');
                var dstInfo = [];
                if (c.orig_dst_host) dstInfo.push(c.orig_dst_host);
                if (c.orig_dst_city && c.orig_dst_geo) dstInfo.push(countryFlag(c.orig_dst_geo) + ' ' + c.orig_dst_city);
                else if (c.orig_dst_geo) dstInfo.push(countryFlag(c.orig_dst_geo) + ' ' + c.orig_dst_geo);
                if (c.orig_dst_asn) dstInfo.push(c.orig_dst_asn);
                var dstHtml = '<div style="font-family:var(--font-mono,monospace);font-size:11px">' + dstAddr + '</div>';
                if (dstInfo.length) dstHtml += '<div style="font-size:9px;color:var(--text-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:200px">' + dstInfo.join(' &middot; ') + '</div>';
                // Family badge
                var familyBadge = c.family === 'ipv6' ? '<span style="font-size:9px;color:var(--text-2);background:var(--bg-2);padding:1px 4px;border-radius:3px;margin-left:4px">v6</span>' : '';
                h += '<tr' + rowBg + '>';
                h += '<td style="padding:6px 10px">' + (c.protocol || '').toUpperCase() + familyBadge + '</td>';
                h += '<td style="padding:6px 10px">' + (c.state || '—') + '</td>';
                h += '<td style="padding:6px 10px">' + srcHtml + '</td>';
                h += '<td style="padding:6px 10px">' + dstHtml + '</td>';
                h += '<td style="padding:6px 10px">' + (c.nat_type || 'none') + '</td>';
                h += '<td style="padding:6px 10px;text-align:right;font-variant-numeric:tabular-nums">' + formatBytes(c.bytes || 0) + '</td>';
                h += '<td style="padding:6px 10px;text-align:right;font-variant-numeric:tabular-nums;color:var(--text-2)">' + (c.packets ? c.packets.toLocaleString() : '—') + '</td>';
                h += '</tr>';
            }
            h += '</tbody></table></div>';
        } else {
            h += '<div style="text-align:center;padding:20px;color:var(--text-2);font-size:13px">No active connections tracked for this host</div>';
        }

        bodyEl.innerHTML = h;
    }

    function renderHostHistoryChart(history) {
        var W = 760, H = 175, ML = 55, PT = 8, PB = 28;
        var chartW = W - ML;
        var maxVal = 1;
        for (var i = 0; i < history.length; i++) {
            if (history[i].bytes > maxVal) maxVal = history[i].bytes;
        }
        maxVal *= 1.2;

        function yPx(val) { return PT + (1 - val / maxVal) * (H - PT - PB); }

        var svg = '<svg viewBox="0 0 ' + W + ' ' + H + '" style="width:100%;height:' + H + 'px;display:block;border-radius:6px;background:var(--bg-1)">';

        // Y-axis labels
        var yTicks = [0, maxVal * 0.5, maxVal];
        for (var yi = 0; yi < yTicks.length; yi++) {
            var y = yPx(yTicks[yi]);
            svg += '<line x1="' + ML + '" y1="' + y.toFixed(1) + '" x2="' + W + '" y2="' + y.toFixed(1) + '" stroke="var(--text-3)" stroke-width="0.5" opacity="0.25"/>';
            svg += '<text x="' + (ML - 3) + '" y="' + (y + 3) + '" text-anchor="end" fill="var(--text-2)" font-size="9px">' + formatBytes(yTicks[yi]) + '</text>';
        }

        // RX area + line (cyan)
        var rxPts = [], txPts = [];
        for (var i = 0; i < history.length; i++) {
            var x = ML + (i / (history.length - 1)) * chartW;
            rxPts.push({ x: x, y: yPx(history[i].rx_bytes || 0) });
            txPts.push({ x: x, y: yPx(history[i].tx_bytes || 0) });
        }

        function smoothPath(pts) {
            if (pts.length < 2) return '';
            var p = 'M' + pts[0].x.toFixed(1) + ',' + pts[0].y.toFixed(1);
            for (var i = 1; i < pts.length; i++) {
                var cx = (pts[i-1].x + pts[i].x) / 2;
                p += ' C' + cx.toFixed(1) + ',' + pts[i-1].y.toFixed(1) + ' ' + cx.toFixed(1) + ',' + pts[i].y.toFixed(1) + ' ' + pts[i].x.toFixed(1) + ',' + pts[i].y.toFixed(1);
            }
            return p;
        }

        function smoothFill(pts) {
            if (pts.length < 2) return '';
            var p = 'M' + pts[0].x.toFixed(1) + ',' + (H - PB) + ' L' + pts[0].x.toFixed(1) + ',' + pts[0].y.toFixed(1);
            for (var i = 1; i < pts.length; i++) {
                var cx = (pts[i-1].x + pts[i].x) / 2;
                p += ' C' + cx.toFixed(1) + ',' + pts[i-1].y.toFixed(1) + ' ' + cx.toFixed(1) + ',' + pts[i].y.toFixed(1) + ' ' + pts[i].x.toFixed(1) + ',' + pts[i].y.toFixed(1);
            }
            p += ' L' + pts[pts.length-1].x.toFixed(1) + ',' + (H - PB) + ' Z';
            return p;
        }

        svg += '<path d="' + smoothFill(txPts) + '" fill="#a78bfa" opacity="0.1"/>';
        svg += '<path d="' + smoothPath(txPts) + '" fill="none" stroke="#a78bfa" stroke-width="1.5" stroke-linejoin="round"/>';
        svg += '<path d="' + smoothFill(rxPts) + '" fill="#22d3ee" opacity="0.1"/>';
        svg += '<path d="' + smoothPath(rxPts) + '" fill="none" stroke="#22d3ee" stroke-width="1.5" stroke-linejoin="round"/>';

        // Legend (top-right)
        svg += '<circle cx="' + (W - 48) + '" cy="' + (PT + 6) + '" r="3" fill="#22d3ee"/>';
        svg += '<text x="' + (W - 42) + '" y="' + (PT + 9) + '" fill="var(--text-2)" font-size="8px">RX</text>';
        svg += '<circle cx="' + (W - 22) + '" cy="' + (PT + 6) + '" r="3" fill="#a78bfa"/>';
        svg += '<text x="' + (W - 16) + '" y="' + (PT + 9) + '" fill="var(--text-2)" font-size="8px">TX</text>';

        // X-axis time labels
        if (history.length > 1 && history[0].ts) {
            var pad2 = function(n) { return n < 10 ? '0' + n : '' + n; };
            var timeFmt = function(d) { return pad2(d.getHours()) + ':' + pad2(d.getMinutes()); };
            var nLabels = Math.min(6, Math.max(2, Math.floor(history.length / 60)));
            for (var li = 0; li < nLabels; li++) {
                var frac = li / (nLabels - 1);
                var idx = Math.round(frac * (history.length - 1));
                var lx = ML + (idx / (history.length - 1)) * chartW;
                var anchor = li === 0 ? 'start' : (li === nLabels - 1 ? 'end' : 'middle');
                svg += '<text x="' + lx.toFixed(1) + '" y="' + (H - 3) + '" text-anchor="' + anchor + '" fill="var(--text-3)" font-size="8px">' + timeFmt(new Date(history[idx].ts)) + '</text>';
            }
        }

        svg += '</svg>';
        return svg;
    }

    // Make IPs clickable in talkers tables
    document.addEventListener('click', function(e) {
        var el = e.target.closest('.ip-clickable');
        if (el) {
            e.preventDefault();
            window._openHostModal(el.getAttribute('data-ip'));
        }
    });

    function connect() {
        if (sse) { sse.close(); sse = null; }

        sse = new EventSource('/api/events');

        sse.onopen = function() {
            document.getElementById('statusDot').className = 'status-dot';
            document.getElementById('statusText').textContent = 'Live';
        };
        sse.onerror = function() {
            document.getElementById('statusDot').className = 'status-dot error';
            document.getElementById('statusText').textContent = 'Reconnecting';
        };
        sse.onmessage = function(e) {
            try {
                var d = JSON.parse(e.data);
                if (d.timestamp && (Date.now() - d.timestamp) > 5000) return;
                process(d);
            } catch(ex) { console.error(ex); }
        };
    }

    // Reconnect when the page becomes visible (e.g. after laptop sleep)
    document.addEventListener('visibilitychange', function() {
        if (document.visibilityState === 'visible') {
            if (!sse || sse.readyState === EventSource.CLOSED) {
                connect();
            }
        }
    });

    function process(d) {
        _lastPayload = d;
        var ifaces = d.interfaces || [], bw = d.top_bandwidth || [], vol = d.top_volume || [];
        var rx = 0, tx = 0;
        for (var f of ifaces) { rx += f.rx_rate || 0; tx += f.tx_rate || 0; knownIfaces.add(f.name); }

        renderStatsRow(ifaces, d);

        // VPN routing banner
        var vpnActive = false, vpnSince = '', vpnName = '';
        for (var f of ifaces) {
            if (f.vpn_routing) {
                vpnActive = true;
                vpnName = f.name;
                vpnSince = f.vpn_routing_since || '';
                break;
            }
        }
        var banner = document.getElementById('vpnBanner');
        if (vpnActive) {
            banner.className = 'vpn-banner active';
            var txt = 'Traffic routed via ' + vpnName;
            document.getElementById('vpnBannerSince').textContent = vpnSince ? '(since ' + vpnSince + ')' : '';
            banner.querySelector('.vpn-banner-text').firstChild.textContent = txt + ' ';
        } else {
            banner.className = 'vpn-banner inactive';
        }

        var now = new Date();
        for (var f of ifaces) {
            if (!chartData[f.name]) chartData[f.name] = { rx: [], tx: [] };
            // EMA smoothing to reduce 1-second rate jitter
            if (!_emaState[f.name]) _emaState[f.name] = { rx: 0, tx: 0 };
            var em = _emaState[f.name];
            var rawRx = f.rx_rate || 0;
            var rawTx = f.tx_rate || 0;
            em.rx = em.rx === 0 ? rawRx : EMA_ALPHA * rawRx + (1 - EMA_ALPHA) * em.rx;
            em.tx = em.tx === 0 ? rawTx : EMA_ALPHA * rawTx + (1 - EMA_ALPHA) * em.tx;
            chartData[f.name].rx.push({ x: now, y: em.rx });
            chartData[f.name].tx.push({ x: now, y: -(em.tx) });
            if (chartData[f.name].rx.length > MAX_PTS) { chartData[f.name].rx.shift(); chartData[f.name].tx.shift(); }
        }

        renderIfaceCards(ifaces);
        sparklineData = d.sparklines || {};
        drawAllSparklines();
        renderIfaceTabs();
        updateChart();

        // Only update data for the active tab to reduce DOM thrashing.
        // _renderTab handles all tab-specific rendering.
        _renderTab(_activeTab, d);
    }

    function tick() { document.getElementById('clock').textContent = new Date().toLocaleTimeString(); }
    setInterval(tick, 1000); tick();

    // Wire search/sort on WiFi client table to re-render
    var _lastWiFi = null;
    var _origUpdateWiFi = updateWiFi;
    updateWiFi = function(wifi) {
        if (wifi) _lastWiFi = wifi;
        _origUpdateWiFi(wifi);
    };
    ['wifiClientSearch', 'wifiClientSort'].forEach(function(id) {
        var el = document.getElementById(id);
        if (el) el.addEventListener(id === 'wifiClientSearch' ? 'input' : 'change', function() {
            if (_lastWiFi) updateWiFi(_lastWiFi);
        });
    });

    // ── Speed Test ──
    var _stHistoryLoaded = false;
    var _stRunning = false;

    function drawGauge(canvasId, value, max, color) {
        var canvas = document.getElementById(canvasId);
        if (!canvas) return;
        var dpr = window.devicePixelRatio || 1;
        var dw = canvas.offsetWidth, dh = canvas.offsetHeight;
        if (!dw || !dh) return;
        canvas.width = dw * dpr; canvas.height = dh * dpr;
        var c = canvas.getContext('2d');
        c.scale(dpr, dpr);

        var cx = dw / 2, cy = dh - 10;
        var r = Math.min(cx - 10, cy - 5);
        var startAngle = Math.PI;
        var endAngle = 2 * Math.PI;

        // Background arc
        c.beginPath();
        c.arc(cx, cy, r, startAngle, endAngle);
        c.lineWidth = 8;
        c.strokeStyle = getComputedStyle(document.documentElement).getPropertyValue('--bg-3').trim() || '#1f1f23';
        c.lineCap = 'round';
        c.stroke();

        // Value arc
        if (value > 0) {
            var pct = Math.min(value / max, 1);
            var valAngle = startAngle + pct * Math.PI;
            c.beginPath();
            c.arc(cx, cy, r, startAngle, valAngle);
            c.lineWidth = 8;
            c.strokeStyle = color;
            c.lineCap = 'round';
            c.stroke();
        }
    }

    function updateSpeedTestGauges(ping, download, upload, jitter) {
        var pingEl = document.getElementById('stPingValue');
        var downEl = document.getElementById('stDownValue');
        var upEl = document.getElementById('stUpValue');
        var jitterEl = document.getElementById('stJitterValue');

        pingEl.textContent = ping >= 0 ? ping.toFixed(1) : '--';
        downEl.textContent = download >= 0 ? download.toFixed(1) : '--';
        upEl.textContent = upload >= 0 ? upload.toFixed(1) : '--';
        jitterEl.textContent = jitter >= 0 ? jitter.toFixed(1) : '--';

        drawGauge('stPingGauge', ping >= 0 ? ping : 0, 100, '#22d3ee');
        drawGauge('stDownGauge', download >= 0 ? download : 0, 1000, '#3b82f6');
        drawGauge('stUpGauge', upload >= 0 ? upload : 0, 1000, '#a78bfa');
        drawGauge('stJitterGauge', jitter >= 0 ? jitter : 0, 50, '#f59e0b');
    }

    function renderSpeedTestHistory(results) {
        var tb = document.getElementById('speedtestHistory');
        if (!results || !results.length) {
            tb.innerHTML = '<tr><td colspan="6" class="empty-state">No tests yet &mdash; click Start Test</td></tr>';
            return;
        }
        var h = '';
        for (var i = 0; i < results.length; i++) {
            var r = results[i];
            var d = new Date(r.timestamp);
            var dateStr = d.toLocaleDateString() + ' ' + d.toLocaleTimeString();
            h += '<tr>';
            h += '<td><span class="' + rankClass(i) + '">' + (i + 1) + '</span></td>';
            h += '<td style="font-size:12px;white-space:nowrap">' + dateStr + '</td>';
            h += '<td style="font-variant-numeric:tabular-nums;font-weight:600;color:var(--rx)">' + r.download_mbps.toFixed(1) + ' Mbps</td>';
            h += '<td style="font-variant-numeric:tabular-nums;font-weight:600;color:var(--tx)">' + r.upload_mbps.toFixed(1) + ' Mbps</td>';
            h += '<td style="font-variant-numeric:tabular-nums">' + r.ping_ms.toFixed(1) + ' ms</td>';
            h += '<td style="font-variant-numeric:tabular-nums">' + r.jitter_ms.toFixed(1) + ' ms</td>';
            h += '</tr>';
        }
        tb.innerHTML = h;
    }

    function loadSpeedTestHistory() {
        fetch('/api/speedtest/results').then(function(r) { return r.json(); }).then(function(data) {
            _stHistoryLoaded = true;
            if (data.running) {
                document.getElementById('speedtestBtn').disabled = true;
                document.getElementById('speedtestBtn').textContent = 'Running...';
                _stRunning = true;
            }
            renderSpeedTestHistory(data.results || []);
            if (data.results && data.results.length) {
                var last = data.results[0];
                updateSpeedTestGauges(last.ping_ms, last.download_mbps, last.upload_mbps, last.jitter_ms);
            }
        }).catch(function() {});
    }

    window._runSpeedTest = function() {
        if (_stRunning) return;
        _stRunning = true;

        var btn = document.getElementById('speedtestBtn');
        btn.disabled = true;
        btn.innerHTML = '<span class="speedtest-spinner"></span> Running...';

        var wrap = document.getElementById('speedtestProgressWrap');
        wrap.style.display = '';
        var bar = document.getElementById('speedtestProgressBar');
        var phase = document.getElementById('speedtestPhase');
        updateSpeedTestGauges(-1, -1, -1, -1);
        bar.style.width = '0%';
        phase.textContent = 'Connecting...';

        var currentPing = -1;
        var currentDownload = -1;
        var currentUpload = -1;
        var currentJitter = -1;

        fetch('/api/speedtest/run', { method: 'POST' }).then(function(resp) {
            if (resp.status === 409) {
                phase.textContent = 'Test already running';
                return;
            }
            var reader = resp.body.getReader();
            var decoder = new TextDecoder();
            var buffer = '';

            function processChunk() {
                return reader.read().then(function(result) {
                    if (result.done) return;
                    buffer += decoder.decode(result.value, { stream: true });
                    var lines = buffer.split('\n');
                    buffer = lines.pop(); // keep partial line

                    for (var i = 0; i < lines.length; i++) {
                        var line = lines[i].trim();
                        if (!line.startsWith('data: ')) continue;
                        try {
                            var p = JSON.parse(line.substring(6));
                            handleProgress(p);
                        } catch(e) {}
                    }
                    return processChunk();
                });
            }

            function handleProgress(p) {
                if (p.phase === 'ping') {
                    phase.textContent = 'Measuring latency...';
                    bar.style.width = (p.percent * 0.15) + '%';
                    bar.className = 'speedtest-progress-bar-fill ping';
                    if (p.percent >= 100 && p.value > 0) {
                        currentPing = p.value;
                    }
                    updateSpeedTestGauges(currentPing, currentDownload, currentUpload, currentJitter);
                } else if (p.phase === 'download') {
                    currentDownload = p.value;
                    phase.textContent = 'Testing download... ' + p.value.toFixed(1) + ' Mbps';
                    bar.style.width = (15 + p.percent * 0.40) + '%';
                    bar.className = 'speedtest-progress-bar-fill download';
                    updateSpeedTestGauges(currentPing, currentDownload, currentUpload, currentJitter);
                } else if (p.phase === 'upload') {
                    currentUpload = p.value;
                    phase.textContent = 'Testing upload... ' + p.value.toFixed(1) + ' Mbps';
                    bar.style.width = (55 + p.percent * 0.40) + '%';
                    bar.className = 'speedtest-progress-bar-fill upload';
                    updateSpeedTestGauges(currentPing, currentDownload, currentUpload, currentJitter);
                } else if (p.phase === 'done' && p.result) {
                    phase.textContent = 'Complete!';
                    bar.style.width = '100%';
                    bar.className = 'speedtest-progress-bar-fill done';
                    var r = p.result;
                    updateSpeedTestGauges(r.ping_ms, r.download_mbps, r.upload_mbps, r.jitter_ms);
                    loadSpeedTestHistory();
                    finishTest();
                } else if (p.phase === 'error') {
                    phase.textContent = 'Error — test failed';
                    bar.className = 'speedtest-progress-bar-fill error';
                    finishTest();
                }
            }

            return processChunk();
        }).catch(function(e) {
            phase.textContent = 'Connection error';
            finishTest();
        });

        function finishTest() {
            _stRunning = false;
            btn.disabled = false;
            btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="width:14px;height:14px"><polygon points="5 3 19 12 5 21 5 3"/></svg> Start Test';
            setTimeout(function() {
                wrap.style.display = 'none';
            }, 3000);
        }
    };

    // ── Debug: Traceroute ──
    var _trRunning = false;

    window._runTraceroute = function() {
        if (_trRunning) return;
        var target = (document.getElementById('trTarget').value || '').trim();
        if (!target) { alert('Enter an IP or hostname'); return; }
        var count = parseInt(document.getElementById('trCount').value) || 20;

        _trRunning = true;
        var btn = document.getElementById('trBtn');
        btn.disabled = true;
        btn.innerHTML = '<span class="speedtest-spinner"></span> Running...';

        var wrap = document.getElementById('trProgressWrap');
        wrap.style.display = '';
        var bar = document.getElementById('trProgressBar');
        var phase = document.getElementById('trPhase');
        bar.style.width = '0%';
        bar.className = 'speedtest-progress-bar-fill ping';
        phase.textContent = 'Running traceroute...';

        document.getElementById('trResults').style.display = 'none';

        fetch('/api/debug/traceroute?target=' + encodeURIComponent(target) + '&count=' + count, { method: 'POST' })
        .then(function(resp) {
            var reader = resp.body.getReader();
            var decoder = new TextDecoder();
            var buffer = '';

            function processChunk() {
                return reader.read().then(function(result) {
                    if (result.done) return;
                    buffer += decoder.decode(result.value, { stream: true });
                    var lines = buffer.split('\n');
                    buffer = lines.pop();
                    for (var i = 0; i < lines.length; i++) {
                        var line = lines[i].trim();
                        if (!line.startsWith('data: ')) continue;
                        try {
                            var p = JSON.parse(line.substring(6));
                            handleTrProgress(p);
                        } catch(e) {}
                    }
                    return processChunk();
                });
            }

            function handleTrProgress(p) {
                if (p.phase === 'running') {
                    phase.textContent = p.message;
                    if (p.ttl > 0) {
                        bar.style.width = Math.min((p.ttl / 30) * 100, 99) + '%';
                    }
                } else if (p.phase === 'done' && p.result) {
                    phase.textContent = p.message;
                    bar.style.width = '100%';
                    bar.className = 'speedtest-progress-bar-fill done';
                    renderTrResults(p.result);
                    finishTraceroute();
                } else if (p.phase === 'error') {
                    phase.textContent = p.message;
                    bar.className = 'speedtest-progress-bar-fill error';
                    finishTraceroute();
                }
            }

            return processChunk();
        }).catch(function() {
            phase.textContent = 'Connection error';
            finishTraceroute();
        });

        function finishTraceroute() {
            _trRunning = false;
            btn.disabled = false;
            btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="width:14px;height:14px"><polygon points="5 3 19 12 5 21 5 3"/></svg> Run';
            setTimeout(function() { wrap.style.display = 'none'; }, 2000);
        }
    };

    function renderTrResults(result) {
        document.getElementById('trResults').style.display = '';
        document.getElementById('trResultTitle').textContent = 'Traceroute to ' + result.target + ' (' + result.resolved_ip + ')';
        document.getElementById('trResultSub').textContent = result.probes_per_ttl + ' probes/hop — ' + result.hops.length + ' hops' + (result.reached_dest ? ' — destination reached' : '') + (result.error ? ' — ' + result.error : '');

        var tb = document.getElementById('trTable');
        if (!result.hops || !result.hops.length) {
            tb.innerHTML = '<tr><td colspan="5" class="empty-state">No hops received</td></tr>';
            return;
        }

        var h = '';
        for (var i = 0; i < result.hops.length; i++) {
            var hop = result.hops[i];
            var lossClass = hop.loss_pct > 0 ? (hop.loss_pct > 10 ? ' style="color:var(--danger);font-weight:600"' : ' style="color:var(--warning)"') : '';
            var rttStr = hop.received > 0 ? hop.avg_rtt_ms.toFixed(2) + ' ms' : '—';
            if (hop.received > 1) rttStr += ' <span style="color:var(--text-2);font-size:11px">(min ' + hop.min_rtt_ms.toFixed(2) + ' / max ' + hop.max_rtt_ms.toFixed(2) + ')</span>';
            var ipStr = hop.ip || '<span style="color:var(--text-2)">*</span>';
            var hostStr = hop.hostname || '—';

            h += '<tr>';
            h += '<td>' + hop.ttl + '</td>';
            h += '<td style="font-family:JetBrains Mono,monospace;font-size:12px">' + ipStr + '</td>';
            h += '<td style="font-size:12px;color:var(--text-2)">' + hostStr + '</td>';
            h += '<td style="font-variant-numeric:tabular-nums;font-size:12px">' + rttStr + '</td>';
            h += '<td' + lossClass + '>' + hop.loss_pct.toFixed(1) + '%</td>';
            h += '</tr>';
        }
        tb.innerHTML = h;
    }

    // ── Debug: MTU Discovery ──
    var _mtuRunning = false;
    window._runMTUDiscovery = function() {
        if (_mtuRunning) return;
        var target = (document.getElementById('mtuTarget').value || '').trim();
        if (!target) { alert('Enter an IP or hostname'); return; }

        _mtuRunning = true;
        var btn = document.getElementById('mtuBtn');
        btn.disabled = true;
        btn.innerHTML = '<span class="speedtest-spinner"></span> Testing...';

        var wrap = document.getElementById('mtuProgressWrap');
        wrap.style.display = '';
        var bar = document.getElementById('mtuProgressBar');
        var phase = document.getElementById('mtuPhase');
        bar.style.width = '0%';
        bar.className = 'speedtest-progress-bar-fill ping';
        phase.textContent = 'Starting MTU discovery...';

        document.getElementById('mtuResults').style.display = 'none';

        var probeCount = 0;
        fetch('/api/debug/mtu?target=' + encodeURIComponent(target), { method: 'POST' })
        .then(function(resp) {
            var reader = resp.body.getReader();
            var decoder = new TextDecoder();
            var buffer = '';

            function processChunk() {
                return reader.read().then(function(result) {
                    if (result.done) return;
                    buffer += decoder.decode(result.value, { stream: true });
                    var lines = buffer.split('\n');
                    buffer = lines.pop();
                    for (var i = 0; i < lines.length; i++) {
                        var line = lines[i].trim();
                        if (!line.startsWith('data: ')) continue;
                        try {
                            var p = JSON.parse(line.substring(6));
                            handleMTUProgress(p);
                        } catch(e) {}
                    }
                    return processChunk();
                });
            }

            function handleMTUProgress(p) {
                if (p.phase === 'running') {
                    phase.textContent = p.message;
                    probeCount++;
                    // Binary search of 1500 range ~ 11 steps; animate progress
                    bar.style.width = Math.min(probeCount * 8, 99) + '%';
                } else if (p.phase === 'done' && p.result) {
                    phase.textContent = p.message;
                    bar.style.width = '100%';
                    bar.className = 'speedtest-progress-bar-fill done';
                    renderMTUResults(p.result);
                    finishMTU();
                } else if (p.phase === 'error') {
                    phase.textContent = p.message;
                    bar.className = 'speedtest-progress-bar-fill error';
                    finishMTU();
                }
            }

            return processChunk();
        }).catch(function() {
            phase.textContent = 'Connection error';
            finishMTU();
        });

        function finishMTU() {
            _mtuRunning = false;
            btn.disabled = false;
            btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="width:14px;height:14px"><path d="M12 2v20M2 12h20"/></svg> Discover';
            setTimeout(function() { wrap.style.display = 'none'; }, 2000);
        }
    };

    function renderMTUResults(result) {
        document.getElementById('mtuResults').style.display = '';
        var mtu = result.path_mtu;
        var titleText = 'Path MTU to ' + result.target + ' (' + result.resolved_ip + ')';
        document.getElementById('mtuResultTitle').textContent = titleText;

        var subParts = [];
        if (mtu > 0) {
            subParts.push('Path MTU: ' + mtu + ' bytes');
        } else {
            subParts.push('Could not determine path MTU');
        }
        if (result.local_mtu > 0) subParts.push('Local interface MTU: ' + result.local_mtu + ' bytes');
        subParts.push(result.probes.length + ' probes sent');
        document.getElementById('mtuResultSub').textContent = subParts.join(' — ');

        var body = document.getElementById('mtuResultBody');
        var h = '';

        // Summary banner
        if (mtu > 0) {
            var mtuColor = mtu >= 1500 ? 'var(--success)' : (mtu >= 1400 ? 'var(--warning)' : 'var(--danger)');
            var mtuNote = '';
            if (mtu >= 1500) {
                mtuNote = 'Standard Ethernet MTU — no issues expected';
            } else if (mtu >= 1400) {
                mtuNote = 'Slightly reduced — common with VPN/PPPoE tunnels';
            } else if (mtu >= 1280) {
                mtuNote = 'Below standard — possible tunnel or misconfigured link';
            } else {
                mtuNote = 'Very low MTU — likely causing performance issues';
            }
            h += '<div style="padding:16px 20px;border-bottom:1px solid var(--border);display:flex;align-items:center;gap:16px">';
            h += '<div style="font-size:32px;font-weight:700;font-family:JetBrains Mono,monospace;color:' + mtuColor + '">' + mtu + '</div>';
            h += '<div>';
            h += '<div style="font-size:13px;font-weight:600;color:var(--text-0)">bytes path MTU</div>';
            h += '<div style="font-size:12px;color:var(--text-2);margin-top:2px">' + mtuNote + '</div>';
            if (result.local_mtu > 0 && result.local_mtu !== mtu) {
                h += '<div style="font-size:11px;color:var(--warning);margin-top:4px">⚠ Local interface MTU (' + result.local_mtu + ') differs from path MTU (' + mtu + ')</div>';
            }
            h += '</div></div>';
        } else {
            h += '<div style="padding:16px 20px;border-bottom:1px solid var(--border);color:var(--warning);font-size:13px">';
            h += '⚠ Could not determine path MTU — host may be unreachable or blocking ICMP</div>';
        }

        // Probe table
        if (result.probes && result.probes.length) {
            h += '<div style="max-height:400px;overflow-y:auto">';
            h += '<table><thead><tr>';
            h += '<th>Packet Size</th><th>Result</th><th>RTT (ms)</th><th>Detail</th>';
            h += '</tr></thead><tbody>';
            for (var i = 0; i < result.probes.length; i++) {
                var p = result.probes[i];
                var statusColor = p.success ? 'var(--success)' : 'var(--danger)';
                var statusIcon = p.success ? '✓ Pass' : '✗ Blocked';
                var bg = p.size === mtu ? 'background:color-mix(in srgb, var(--success) 10%, transparent)' : '';
                h += '<tr style="' + bg + '">';
                h += '<td style="font-family:JetBrains Mono,monospace;font-size:12px">' + p.size + ' bytes' + (p.size === mtu ? ' ←' : '') + '</td>';
                h += '<td style="color:' + statusColor + ';font-weight:600;font-size:12px">' + statusIcon + '</td>';
                h += '<td style="font-variant-numeric:tabular-nums;font-size:12px">' + (p.rtt_ms > 0 ? p.rtt_ms.toFixed(2) + ' ms' : '—') + '</td>';
                h += '<td style="font-size:11px;color:var(--text-2)">' + (p.error || '') + '</td>';
                h += '</tr>';
            }
            h += '</tbody></table></div>';
        }

        body.innerHTML = h;
    }

    // ── Debug: DNS Check ──
    window._runDNSCheck = function() {
        var domain = (document.getElementById('dnsCheckDomain').value || '').trim();
        if (!domain) { alert('Enter a domain'); return; }
        var qtype = document.getElementById('dnsCheckType').value;

        var btn = document.getElementById('dnsCheckBtn');
        btn.disabled = true;
        btn.innerHTML = '<span class="speedtest-spinner"></span> Querying...';

        fetch('/api/debug/dns?domain=' + encodeURIComponent(domain) + '&type=' + qtype)
        .then(function(r) { return r.json(); })
        .then(function(data) {
            renderDNSCheckResults(data);
            btn.disabled = false;
            btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="width:14px;height:14px"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg> Query';
        }).catch(function() {
            btn.disabled = false;
            btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="width:14px;height:14px"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg> Query';
        });
    };

    function renderDNSCheckResults(data) {
        var wrap = document.getElementById('dnsCheckResults');
        wrap.style.display = '';
        document.getElementById('dnsCheckTitle').textContent = data.domain + ' (' + data.type + ')';
        document.getElementById('dnsCheckSub').textContent = data.servers.length + ' DNS servers queried';

        var body = document.getElementById('dnsCheckBody');
        var h = '';

        // Resolver leak check info
        if (data.resolver_info) {
            var ri = data.resolver_info;
            h += '<div style="border-bottom:1px solid var(--border);padding:14px 20px;background:var(--bg-2)">';
            h += '<div style="font-size:12px;font-weight:600;color:var(--text-0);margin-bottom:8px">Resolver Chain</div>';

            // Show the configured local resolver
            if (ri.configured_resolver) {
                h += '<div style="display:flex;align-items:center;gap:8px;margin-bottom:10px">';
                h += '<span style="font-size:11px;color:var(--text-2);min-width:110px">Local resolver:</span>';
                h += '<span style="font-family:JetBrains Mono,monospace;font-size:12px;padding:3px 8px;border-radius:4px;background:var(--bg-3);color:var(--accent)">' + ri.configured_resolver + '</span>';
                h += '<span style="font-size:11px;color:var(--text-2)">(from /etc/resolv.conf)</span>';
                h += '</div>';
            }

            // Show upstream resolver IPs as seen by authoritative servers
            h += '<div style="display:flex;align-items:baseline;gap:8px;margin-bottom:4px">';
            h += '<span style="font-size:11px;color:var(--text-2);min-width:110px">Upstream egress:</span>';
            h += '<div style="display:flex;flex-wrap:wrap;gap:4px">';
            if (ri.resolver_ips && ri.resolver_ips.length) {
                for (var i = 0; i < ri.resolver_ips.length; i++) {
                    h += '<span style="font-family:JetBrains Mono,monospace;font-size:12px;padding:3px 8px;border-radius:4px;background:var(--bg-3);color:var(--text-0)">' + ri.resolver_ips[i] + '</span>';
                }
            } else {
                h += '<span style="font-size:12px;color:var(--warning)">' + (ri.error || 'No resolver IPs detected') + '</span>';
            }
            h += '</div></div>';
            h += '<div style="font-size:10px;color:var(--text-2);margin-top:4px;margin-bottom:6px;padding-left:118px">IPs seen by authoritative DNS servers (o-o.myaddr.l.google.com, dnscheck.tools)</div>';

            if (ri.ecs && ri.ecs.length) {
                h += '<div style="display:flex;align-items:baseline;gap:8px;margin-top:6px">';
                h += '<span style="font-size:11px;color:var(--text-2);min-width:110px">ECS:</span>';
                h += '<div style="display:flex;flex-wrap:wrap;gap:4px">';
                for (var ei = 0; ei < ri.ecs.length; ei++) {
                    h += '<span style="font-family:JetBrains Mono,monospace;font-size:11px;padding:2px 6px;border-radius:3px;background:var(--bg-3);color:var(--text-2)">' + ri.ecs[ei] + '</span>';
                }
                h += '</div></div>';
            }
            if (ri.dnscheck_info) {
                var dc = ri.dnscheck_info;
                h += '<div style="margin-top:10px;display:grid;grid-template-columns:110px 1fr;gap:3px 8px;font-size:12px">';
                var dcFields = [['resolverOrg', 'Upstream org:'], ['resolverGeo', 'Upstream geo:'], ['proto', 'Protocol:'], ['edns0', 'EDNS0:']];
                for (var di = 0; di < dcFields.length; di++) {
                    var key = dcFields[di][0], label = dcFields[di][1];
                    if (dc[key]) {
                        h += '<span style="color:var(--text-2);font-size:11px">' + label + '</span>';
                        h += '<span style="font-family:JetBrains Mono,monospace;font-size:11px;color:var(--text-0)">' + dc[key] + '</span>';
                    }
                }
                h += '</div>';
            }
            h += '</div>';
        }

        // Sort servers: System Resolver first, then alphabetically by name
        var servers = (data.servers || []).slice();
        servers.sort(function(a, b) {
            if (a.server === 'System Resolver') return -1;
            if (b.server === 'System Resolver') return 1;
            return a.server.localeCompare(b.server);
        });

        // Find fastest latency for highlighting
        var fastestLatency = Infinity;
        for (var fi = 0; fi < servers.length; fi++) {
            if (servers[fi].latency_ms > 0 && servers[fi].latency_ms < fastestLatency) fastestLatency = servers[fi].latency_ms;
        }

        // Collect all unique record values across servers to highlight differences
        var allValues = {};
        for (var ai = 0; ai < servers.length; ai++) {
            if (servers[ai].records) {
                for (var ari = 0; ari < servers[ai].records.length; ari++) {
                    var v = servers[ai].records[ari].value;
                    allValues[v] = (allValues[v] || 0) + 1;
                }
            }
        }

        // ── Comparison Matrix ──
        // Collect unique record values across all servers
        var uniqueRecords = [];
        var recordSeen = {};
        for (var mi = 0; mi < servers.length; mi++) {
            if (servers[mi].records) {
                for (var mri = 0; mri < servers[mi].records.length; mri++) {
                    var key = servers[mi].records[mri].type + ':' + servers[mi].records[mri].value;
                    if (!recordSeen[key]) {
                        recordSeen[key] = true;
                        uniqueRecords.push({ type: servers[mi].records[mri].type, value: servers[mi].records[mri].value });
                    }
                }
            }
        }

        if (servers.length > 1) {
            // Short server labels
            var shortNames = servers.map(function(s) {
                return s.server.replace(/ \(.*\)/, '').replace('System Resolver', 'System');
            });

            h += '<div style="border-bottom:1px solid var(--border);padding:14px 16px;overflow-x:auto">';
            h += '<div style="font-size:12px;font-weight:600;color:var(--text-0);margin-bottom:10px">Comparison Matrix</div>';
            h += '<table style="width:100%;border-collapse:collapse;font-size:11px">';

            // Header row: server names
            h += '<thead><tr><th style="text-align:left;padding:4px 8px;font-weight:600;color:var(--text-2);border-bottom:1px solid var(--border);min-width:80px">Record</th>';
            for (var ci = 0; ci < servers.length; ci++) {
                var sColor = servers[ci].rcode === 'NOERROR' ? 'var(--success)' : 'var(--danger)';
                h += '<th style="text-align:center;padding:4px 4px;font-weight:600;color:var(--text-0);border-bottom:1px solid var(--border);white-space:nowrap;font-size:10px">';
                h += '<span style="display:inline-block;width:6px;height:6px;border-radius:50%;background:' + sColor + ';margin-right:3px;vertical-align:middle"></span>';
                h += shortNames[ci] + '</th>';
            }
            h += '</tr></thead><tbody>';

            // Latency row
            h += '<tr><td style="padding:4px 8px;font-weight:600;color:var(--text-2)">Latency</td>';
            for (var ci = 0; ci < servers.length; ci++) {
                var lat = servers[ci].latency_ms;
                var lc = lat > 0 && Math.abs(lat - fastestLatency) < 0.1 ? 'var(--success);font-weight:700' : (lat > 100 ? 'var(--warning)' : 'var(--text-0)');
                h += '<td style="text-align:center;padding:4px;font-family:JetBrains Mono,monospace;font-size:10px;color:' + lc + '">' + (lat > 0 ? lat.toFixed(1) : '—') + '</td>';
            }
            h += '</tr>';

            // RCODE row
            h += '<tr style="background:var(--bg-2)"><td style="padding:4px 8px;font-weight:600;color:var(--text-2)">Status</td>';
            for (var ci = 0; ci < servers.length; ci++) {
                var rc = servers[ci].rcode || 'ERROR';
                var rcc = rc === 'NOERROR' ? 'var(--success)' : (rc === 'NXDOMAIN' ? 'var(--danger)' : 'var(--warning)');
                h += '<td style="text-align:center;padding:4px;font-size:10px;font-weight:600;color:' + rcc + '">' + rc + '</td>';
            }
            h += '</tr>';

            // Record rows: each unique value, check which servers have it
            for (var uri = 0; uri < uniqueRecords.length; uri++) {
                var rec = uniqueRecords[uri];
                var bg = uri % 2 === 0 ? '' : 'background:var(--bg-2)';
                // Count how many servers have this record
                var hasCount = 0;
                for (var ci = 0; ci < servers.length; ci++) {
                    if (servers[ci].records) {
                        for (var rci = 0; rci < servers[ci].records.length; rci++) {
                            if (servers[ci].records[rci].type === rec.type && servers[ci].records[rci].value === rec.value) { hasCount++; break; }
                        }
                    }
                }
                var isPartial = hasCount > 0 && hasCount < servers.length;
                h += '<tr style="' + bg + '"><td style="padding:4px 8px;font-family:JetBrains Mono,monospace;font-size:10px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:200px" title="' + rec.value + '">';
                h += '<span style="color:var(--text-2);margin-right:4px">' + rec.type + '</span>' + rec.value + '</td>';
                for (var ci = 0; ci < servers.length; ci++) {
                    var found = false;
                    if (servers[ci].records) {
                        for (var rci = 0; rci < servers[ci].records.length; rci++) {
                            if (servers[ci].records[rci].type === rec.type && servers[ci].records[rci].value === rec.value) { found = true; break; }
                        }
                    }
                    if (found) {
                        h += '<td style="text-align:center;padding:4px;color:var(--success)">✓</td>';
                    } else {
                        h += '<td style="text-align:center;padding:4px;color:' + (isPartial ? 'var(--danger)' : 'var(--text-3)') + '">' + (isPartial ? '✗' : '—') + '</td>';
                    }
                }
                h += '</tr>';
            }
            h += '</tbody></table></div>';
        }

        // ── Per-server detail cards ──
        h += '<div style="font-size:12px;font-weight:600;color:var(--text-2);padding:14px 20px 6px">Server Details</div>';

        for (var si = 0; si < servers.length; si++) {
            var srv = servers[si];
            var srvName = srv.server;
            var latencyStr = srv.latency_ms > 0 ? srv.latency_ms.toFixed(1) + ' ms' : '—';
            var isFastest = srv.latency_ms > 0 && Math.abs(srv.latency_ms - fastestLatency) < 0.1;
            var rcodeColor = srv.rcode === 'NOERROR' ? 'var(--success)' : (srv.rcode === 'NXDOMAIN' ? 'var(--danger)' : 'var(--warning)');
            var latencyColor = isFastest ? 'var(--success)' : (srv.latency_ms > 100 ? 'var(--warning)' : 'var(--text-2)');

            h += '<div class="dns-check-server">';

            // Server header bar
            h += '<div class="dns-check-server-header">';
            h += '<div style="display:flex;align-items:center;gap:8px">';
            h += '<span class="dns-check-server-dot" style="background:' + rcodeColor + '"></span>';
            h += '<span style="font-size:13px;font-weight:600;color:var(--text-0)">' + srvName + '</span>';
            if (srv.ad) h += '<span class="dns-check-badge dnssec">DNSSEC</span>';
            if (srv.truncated) h += '<span class="dns-check-badge truncated">TRUNCATED</span>';
            h += '</div>';
            h += '<div style="display:flex;align-items:center;gap:16px">';
            h += '<span class="dns-check-rcode" style="color:' + rcodeColor + '">' + (srv.rcode || 'ERROR') + '</span>';
            h += '<span class="dns-check-latency" style="color:' + latencyColor + '">' + latencyStr + (isFastest ? ' ⚡' : '') + '</span>';
            h += '</div>';
            h += '</div>';

            if (srv.error) {
                h += '<div class="dns-check-error">' + srv.error + '</div>';
            }

            if (srv.records && srv.records.length) {
                h += '<div class="dns-check-records">';
                for (var ri = 0; ri < srv.records.length; ri++) {
                    var rec = srv.records[ri];
                    // Highlight values that differ from majority
                    var isUnique = allValues[rec.value] === 1 && Object.keys(allValues).length > 1;
                    h += '<div class="dns-check-record' + (isUnique ? ' unique' : '') + '">';
                    h += '<span class="dns-check-rec-type">' + rec.type + '</span>';
                    h += '<span class="dns-check-rec-value">' + rec.value + '</span>';
                    h += '<span class="dns-check-rec-ttl">' + (rec.ttl > 0 ? rec.ttl + 's' : '—') + '</span>';
                    h += '</div>';
                }
                h += '</div>';
            } else if (!srv.error) {
                h += '<div style="padding:8px 16px;font-size:12px;color:var(--text-2)">No records returned</div>';
            }

            h += '</div>';
        }
        body.innerHTML = h;
    }

    connect();

    // ── Network Topology Tab ──────────────────────────────────────────

    var _lastTopology = null;
    var _lastBandwidth = [];

    function updateNetwork(topo, bandwidth) {
        if (!topo || !topo.nodes || !topo.nodes.length) {
            document.getElementById('networkNoData').style.display = '';
            document.getElementById('networkHasData').style.display = 'none';
            return;
        }
        document.getElementById('networkNoData').style.display = 'none';
        document.getElementById('networkHasData').style.display = '';

        _lastTopology = topo;
        _lastBandwidth = bandwidth || [];

        // Summary stats
        var nodes = topo.nodes || [];
        var aps = nodes.filter(function(n) { return n.type === 'ap'; });
        var clients = nodes.filter(function(n) { return n.type === 'client'; });
        var gw = nodes.filter(function(n) { return n.type === 'gateway'; });
        var sources = {};
        nodes.forEach(function(n) {
            (n.source || '').split(',').forEach(function(s) { if (s) sources[s] = true; });
        });

        document.getElementById('netTotalNodes').textContent = nodes.length;
        document.getElementById('netTotalAPs').textContent = aps.length;
        document.getElementById('netTotalClients').textContent = clients.length;
        document.getElementById('netSources').textContent = Object.keys(sources).join(', ') || '—';

        if (gw.length > 0) {
            var gwn = gw[0];
            document.getElementById('netGateway').textContent = gwn.hostname || (gwn.ips && gwn.ips[0]) || gwn.mac || '—';
        } else {
            document.getElementById('netGateway').textContent = '—';
        }

        document.getElementById('networkClientCount').textContent = nodes.length + ' nodes discovered';

        renderNetworkTopology(topo, _lastBandwidth);
        window._filterNetworkClients();
    }

    // ── Client table rendering ──

    window._filterNetworkClients = function() {
        if (!_lastTopology) return;
        var nodes = (_lastTopology.nodes || []).slice();
        var filter = ((document.getElementById('networkClientSearch') || {}).value || '').toLowerCase();
        var sortKey = ((document.getElementById('networkClientSort') || {}).value || 'type');

        if (filter) {
            nodes = nodes.filter(function(n) {
                return (n.hostname || '').toLowerCase().indexOf(filter) !== -1 ||
                       (n.ips || []).join(' ').toLowerCase().indexOf(filter) !== -1 ||
                       (n.mac || '').toLowerCase().indexOf(filter) !== -1 ||
                       (n.type || '').toLowerCase().indexOf(filter) !== -1 ||
                       (n.ssid || '').toLowerCase().indexOf(filter) !== -1 ||
                       (n.source || '').toLowerCase().indexOf(filter) !== -1;
            });
        }

        if (sortKey === 'name') {
            nodes.sort(function(a, b) { return (a.hostname || a.mac || '').localeCompare(b.hostname || b.mac || ''); });
        } else if (sortKey === 'ip') {
            nodes.sort(function(a, b) { return ((a.ips||[])[0]||'').localeCompare(((b.ips||[])[0]||''), undefined, {numeric:true}); });
        } else if (sortKey === 'source') {
            nodes.sort(function(a, b) { return (a.source||'').localeCompare(b.source||''); });
        }
        // default 'type': already sorted by backend

        var tb = document.getElementById('networkClientTable');
        if (!nodes.length) {
            tb.innerHTML = '<tr><td colspan="9" class="empty-state">' + (filter ? 'No matching nodes' : 'No nodes discovered') + '</td></tr>';
            return;
        }
        var h = '';
        for (var i = 0; i < nodes.length; i++) {
            var n = nodes[i];
            var typeIcon = nodeTypeIcon(n.type);
            var sigStr = '';
            if (n.signal && n.signal !== 0) {
                sigStr = n.signal + ' dBm';
            }
            h += '<tr>';
            h += '<td><span class="net-type-badge net-type-' + n.type + '">' + typeIcon + ' ' + n.type + '</span></td>';
            h += '<td>' + (n.hostname || '<span style="color:var(--text-2)">—</span>') + '</td>';
            h += '<td style="font-family:JetBrains Mono,monospace;font-size:12px">' + formatClickableIPs(n.ips) + '</td>';
            h += '<td style="font-family:JetBrains Mono,monospace;font-size:12px">' + (n.mac || '—') + '</td>';
            h += '<td>' + (n.iface || '—') + '</td>';
            h += '<td>' + (n.ssid || '—') + '</td>';
            h += '<td>' + (sigStr || '—') + '</td>';
            h += '<td>' + (n.state || '—') + '</td>';
            h += '<td><span style="font-size:11px">' + (n.source || '—') + '</span></td>';
            h += '</tr>';
        }
        tb.innerHTML = h;
    };

    function formatClickableIPs(ips) {
        if (!ips || !ips.length) return '—';
        return ips.map(function(ip) {
            return '<span class="ip-clickable" data-ip="' + ip + '">' + ip + '</span>';
        }).join('<br>');
    }

    function nodeTypeIcon(type) {
        switch (type) {
            case 'gateway': return '🌐';
            case 'wan_gw': return '🏢';
            case 'tunnel': return '🔒';
            case 'switch': return '🔲';
            case 'ap': return '📡';
            case 'self': return '💻';
            case 'client': return '📱';
            default: return '❓';
        }
    }

    // ── SVG Topology Visualization ──

    var _networkLayout = 'tree';
    window._updateNetworkLayout = function() {
        _networkLayout = (document.getElementById('networkLayout') || {}).value || 'tree';
        if (_lastTopology) renderNetworkTopology(_lastTopology, _lastBandwidth);
    };

    function renderNetworkTopology(topo, bandwidth) {
        var svg = document.getElementById('networkTopologySVG');
        if (!svg) return;
        var container = document.getElementById('networkTopologyContainer');
        var W = container.clientWidth || 800;
        var nodes = topo.nodes || [];
        var links = topo.links || [];
        bandwidth = bandwidth || [];

        if (nodes.length === 0) {
            svg.innerHTML = '<text x="50%" y="50%" text-anchor="middle" fill="var(--text-2)" font-size="14">No topology data</text>';
            return;
        }

        // Build IP → rate lookup from bandwidth data
        var ipRates = {};
        for (var bi = 0; bi < bandwidth.length; bi++) {
            var t = bandwidth[bi];
            if (t.ip) ipRates[t.ip] = { rx: t.rx_rate || 0, tx: t.tx_rate || 0, total: t.rate_bytes || 0, hostname: t.hostname || '' };
        }

        // Build node → rate lookup (match any IP of a node)
        var nodeRates = {};
        var maxNodeRate = 1;
        nodes.forEach(function(n) {
            var best = { rx: 0, tx: 0, total: 0 };
            (n.ips || []).forEach(function(ip) {
                if (ipRates[ip] && ipRates[ip].total > best.total) best = ipRates[ip];
            });
            if (best.total > 0) {
                nodeRates[n.id] = best;
                if (best.total > maxNodeRate) maxNodeRate = best.total;
            }
        });

        // Build adjacency and node lookup
        var nodeById = {};
        nodes.forEach(function(n) { nodeById[n.id] = n; });

        // Find the topmost root: tunnel > wan_gw > gateway > self > first node
        var rootId = null;
        var typePriority = { 'tunnel': 0, 'wan_gw': 1, 'gateway': 2, 'self': 3 };
        var bestPrio = 99;
        nodes.forEach(function(n) {
            var p = typePriority[n.type];
            if (p !== undefined && p < bestPrio) {
                bestPrio = p;
                rootId = n.id;
            }
        });
        if (!rootId) rootId = topo.gateway || topo.self_node || nodes[0].id;
        if (!nodeById[rootId]) rootId = nodes[0].id;

        // Build children map from links — follow source→target direction.
        // Links are: tunnel→wan_gw, wan_gw→self, self→ap, ap→client, etc.
        var childrenMap = {};
        var linkedSet = {};
        links.forEach(function(l) {
            var parent = l.source, child = l.target;
            if (!childrenMap[parent]) childrenMap[parent] = [];
            childrenMap[parent].push({ id: child, link: l });
            linkedSet[child] = true;
            linkedSet[parent] = true;
        });

        // Assign orphans under root
        nodes.forEach(function(n) {
            if (!linkedSet[n.id] && n.id !== rootId) {
                if (!childrenMap[rootId]) childrenMap[rootId] = [];
                childrenMap[rootId].push({ id: n.id, link: { source: rootId, target: n.id, type: 'wired' } });
            }
        });

        // Sort children at every node for stable layout across refreshes
        for (var parentId in childrenMap) {
            childrenMap[parentId].sort(function(a, b) { return a.id < b.id ? -1 : a.id > b.id ? 1 : 0; });
        }

        // Compute layout
        var layout = _networkLayout;
        var nodePositions = {};

        if (layout === 'radial') {
            computeRadialLayout(rootId, childrenMap, nodeById, nodePositions, W, nodes.length);
        } else {
            computeTreeLayout(rootId, childrenMap, nodeById, nodePositions, W, nodes.length);
        }

        // Compute SVG height from positions
        var maxY = 100;
        for (var id in nodePositions) {
            if (nodePositions[id].y + 40 > maxY) maxY = nodePositions[id].y + 40;
        }
        var H = Math.max(500, maxY + 60);
        svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
        svg.style.height = H + 'px';

        var svgHtml = '';
        var textCol = getComputedStyle(document.documentElement).getPropertyValue('--text-0').trim() || '#fafafa';
        var textCol2 = getComputedStyle(document.documentElement).getPropertyValue('--text-2').trim() || '#71717a';
        var lineCol = getComputedStyle(document.documentElement).getPropertyValue('--border').trim() || '#27272a';
        var rxCol = getComputedStyle(document.documentElement).getPropertyValue('--rx').trim() || '#22d3ee';

        // Draw links
        var rxColor = '#34d399';
        var txColor = '#f97316';
        links.forEach(function(l) {
            var from = nodePositions[l.source];
            var to = nodePositions[l.target];
            if (!from || !to) return;
            var dash = '';
            var col = lineCol;
            var sw = '1.5';
            if (l.type === 'wireless') { dash = 'stroke-dasharray="6 4"'; col = rxCol; }
            else if (l.type === 'tunnel') { dash = 'stroke-dasharray="8 3"'; col = '#8b5cf6'; sw = '2'; }
            else if (l.type === 'wan') { col = '#f97316'; sw = '2'; }

            // Check if the child node has traffic
            var childRate = nodeRates[l.target] || nodeRates[l.source];
            if (childRate && childRate.total > 0) {
                // Animated traffic flow line
                var ratio = Math.min(childRate.total / maxNodeRate, 1);
                var flowSw = (1.5 + ratio * 3).toFixed(1);

                // Compute perpendicular offset so RX/TX lines don't overlap
                var dx = to.x - from.x;
                var dy = to.y - from.y;
                var len = Math.sqrt(dx * dx + dy * dy) || 1;
                var perpX = -dy / len;
                var perpY = dx / len;
                var sep = Math.max(2, Math.min(5, parseFloat(flowSw) * 0.8));

                // Base line (dim center line)
                svgHtml += '<line x1="' + from.x + '" y1="' + from.y + '" x2="' + to.x + '" y2="' + to.y + '" stroke="' + lineCol + '" stroke-width="1" opacity="0.15"/>';

                // RX flow (download: parent → child direction, green) — offset left
                if (childRate.rx > 0) {
                    var rxOp = (0.3 + (childRate.rx / maxNodeRate) * 0.5).toFixed(2);
                    var rx1x = (from.x + perpX * sep).toFixed(1), rx1y = (from.y + perpY * sep).toFixed(1);
                    var rx2x = (to.x + perpX * sep).toFixed(1), rx2y = (to.y + perpY * sep).toFixed(1);
                    svgHtml += '<line x1="' + rx1x + '" y1="' + rx1y + '" x2="' + rx2x + '" y2="' + rx2y + '" stroke="' + rxColor + '" stroke-width="' + flowSw + '" stroke-dasharray="6 4" opacity="' + rxOp + '" stroke-linecap="round"><animate attributeName="stroke-dashoffset" from="0" to="-20" dur="1.5s" repeatCount="indefinite"/></line>';
                }
                // TX flow (upload: child → parent direction, orange) — offset right
                if (childRate.tx > 0) {
                    var txOp = (0.3 + (childRate.tx / maxNodeRate) * 0.5).toFixed(2);
                    var tx1x = (to.x - perpX * sep).toFixed(1), tx1y = (to.y - perpY * sep).toFixed(1);
                    var tx2x = (from.x - perpX * sep).toFixed(1), tx2y = (from.y - perpY * sep).toFixed(1);
                    svgHtml += '<line x1="' + tx1x + '" y1="' + tx1y + '" x2="' + tx2x + '" y2="' + tx2y + '" stroke="' + txColor + '" stroke-width="' + flowSw + '" stroke-dasharray="6 4" opacity="' + txOp + '" stroke-linecap="round"><animate attributeName="stroke-dashoffset" from="0" to="-20" dur="1.5s" repeatCount="indefinite"/></line>';
                }

                // Invisible wider hover target with tooltip
                var tip = (childRate.hostname || l.target) + ': \u2193 ' + formatRate(childRate.rx) + ' \u2191 ' + formatRate(childRate.tx);
                svgHtml += '<line x1="' + from.x + '" y1="' + from.y + '" x2="' + to.x + '" y2="' + to.y + '" stroke="transparent" stroke-width="12" style="cursor:pointer" class="net-link-hover" data-tip="' + escSvg(tip) + '"/>';
            } else {
                // Static link (no traffic)
                svgHtml += '<line x1="' + from.x + '" y1="' + from.y + '" x2="' + to.x + '" y2="' + to.y + '" stroke="' + col + '" stroke-width="' + sw + '" ' + dash + ' opacity="0.6"/>';
            }
            if (l.label) {
                var mx = (from.x + to.x) / 2;
                var my = (from.y + to.y) / 2;
                svgHtml += '<text x="' + mx + '" y="' + (my - 4) + '" text-anchor="middle" font-size="9" fill="' + textCol2 + '">' + escSvg(l.label) + '</text>';
            }
        });

        // Draw nodes
        for (var nid in nodePositions) {
            var pos = nodePositions[nid];
            var node = nodeById[nid];
            if (!node) continue;
            var r = nodeRadius(node.type);
            var fill = nodeColor(node.type);
            svgHtml += '<circle cx="' + pos.x + '" cy="' + pos.y + '" r="' + r + '" fill="' + fill + '" stroke="' + fill + '" stroke-width="2" fill-opacity="0.15"/>';
            svgHtml += '<text x="' + pos.x + '" y="' + (pos.y + 4) + '" text-anchor="middle" font-size="' + (r > 14 ? 14 : 11) + '" fill="' + fill + '">' + nodeTypeEmoji(node.type) + '</text>';
            var label = node.hostname || (node.ips && node.ips[0]) || node.mac || node.id;
            if (label.length > 20) label = label.substring(0, 18) + '…';
            svgHtml += '<text x="' + pos.x + '" y="' + (pos.y + r + 14) + '" text-anchor="middle" font-size="11" fill="' + textCol + '">' + escSvg(label) + '</text>';
            svgHtml += '<text x="' + pos.x + '" y="' + (pos.y + r + 26) + '" text-anchor="middle" font-size="9" fill="' + textCol2 + '">' + node.type + '</text>';
        }

        svg.innerHTML = svgHtml;

        // Tooltip for link hover
        var tipEl = document.getElementById('netTooltip');
        if (!tipEl) {
            tipEl = document.createElement('div');
            tipEl.id = 'netTooltip';
            tipEl.style.cssText = 'position:fixed;pointer-events:none;background:var(--card);color:var(--text-0);border:1px solid var(--border);padding:6px 10px;border-radius:6px;font-size:12px;font-family:Inter,sans-serif;z-index:9999;display:none;white-space:nowrap;box-shadow:0 4px 12px rgba(0,0,0,0.15)';
            document.body.appendChild(tipEl);
        }
        svg.addEventListener('mousemove', function(e) {
            var el = e.target.closest('.net-link-hover');
            if (el) {
                tipEl.textContent = el.getAttribute('data-tip');
                tipEl.style.display = '';
                tipEl.style.left = (e.clientX + 12) + 'px';
                tipEl.style.top = (e.clientY - 8) + 'px';
            } else {
                tipEl.style.display = 'none';
            }
        });
        svg.addEventListener('mouseleave', function() { tipEl.style.display = 'none'; });
    }

    function computeTreeLayout(rootId, childrenMap, nodeById, positions, W, totalNodes) {
        var levels = [];
        var visited = {};
        var queue = [{ id: rootId, depth: 0 }];
        visited[rootId] = true;
        while (queue.length > 0) {
            var item = queue.shift();
            if (!levels[item.depth]) levels[item.depth] = [];
            levels[item.depth].push(item.id);
            var kids = childrenMap[item.id] || [];
            for (var i = 0; i < kids.length; i++) {
                if (!visited[kids[i].id]) {
                    visited[kids[i].id] = true;
                    queue.push({ id: kids[i].id, depth: item.depth + 1 });
                }
            }
        }
        for (var id in nodeById) {
            if (!visited[id]) {
                if (!levels[levels.length - 1]) levels.push([]);
                levels[levels.length - 1].push(id);
            }
        }

        var yGap = Math.max(80, Math.min(120, 500 / (levels.length || 1)));
        var padding = 60;
        for (var d = 0; d < levels.length; d++) {
            var row = levels[d];
            var xGap = (W - 2 * padding) / (row.length + 1);
            for (var j = 0; j < row.length; j++) {
                positions[row[j]] = { x: padding + xGap * (j + 1), y: 40 + d * yGap };
            }
        }
    }

    function computeRadialLayout(rootId, childrenMap, nodeById, positions, W, totalNodes) {
        var cx = W / 2, cy = 250;
        positions[rootId] = { x: cx, y: cy };

        var visited = {};
        visited[rootId] = true;
        var rings = [[rootId]];
        var current = [rootId];
        while (current.length > 0) {
            var next = [];
            for (var i = 0; i < current.length; i++) {
                var kids = childrenMap[current[i]] || [];
                for (var j = 0; j < kids.length; j++) {
                    if (!visited[kids[j].id]) {
                        visited[kids[j].id] = true;
                        next.push(kids[j].id);
                    }
                }
            }
            if (next.length > 0) rings.push(next);
            current = next;
        }
        var orphans = [];
        for (var id in nodeById) {
            if (!visited[id]) orphans.push(id);
        }
        if (orphans.length > 0) rings.push(orphans);

        var radiusStep = Math.min(180, (Math.min(W, 500) - 80) / (rings.length || 1));
        for (var ri = 1; ri < rings.length; ri++) {
            var ring = rings[ri];
            var radius = ri * radiusStep;
            for (var ni = 0; ni < ring.length; ni++) {
                var angle = (2 * Math.PI * ni) / ring.length - Math.PI / 2;
                positions[ring[ni]] = {
                    x: cx + radius * Math.cos(angle),
                    y: cy + radius * Math.sin(angle)
                };
            }
        }
    }

    function nodeRadius(type) {
        switch (type) {
            case 'gateway': return 22;
            case 'wan_gw': return 20;
            case 'tunnel': return 20;
            case 'switch': return 18;
            case 'ap': return 18;
            case 'self': return 18;
            default: return 14;
        }
    }

    function nodeColor(type) {
        switch (type) {
            case 'gateway': return '#22d3ee';
            case 'wan_gw': return '#f97316';
            case 'tunnel': return '#8b5cf6';
            case 'switch': return '#a78bfa';
            case 'ap': return '#34d399';
            case 'self': return '#fbbf24';
            case 'client': return '#71717a';
            default: return '#71717a';
        }
    }

    function nodeTypeEmoji(type) {
        switch (type) {
            case 'gateway': return '&#x1F310;';
            case 'wan_gw': return '&#x1F3E2;';
            case 'tunnel': return '&#x1F512;';
            case 'switch': return '&#x1F532;';
            case 'ap': return '&#x1F4E1;';
            case 'self': return '&#x1F4BB;';
            case 'client': return '&#x1F4F1;';
            default: return '?';
        }
    }

    function escSvg(s) {
        return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
    }
})();
