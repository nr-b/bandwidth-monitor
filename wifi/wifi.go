// Package wifi defines the common interface for WiFi controller providers
// (UniFi, Omada, etc.), following the same pattern as the dns package.
package wifi

import "sort"

// Provider is implemented by any WiFi controller stats backend.
type Provider interface {
	GetSummary() *Summary
	Available() bool
	Stop()
}

// Summary is the common WiFi stats format sent to the frontend.
type Summary struct {
	ProviderName string       `json:"provider_name"`
	TotalAPs     int          `json:"total_aps"`
	TotalClients int          `json:"total_clients"`
	APs          []APInfo     `json:"aps"`
	SSIDs        []SSIDStat   `json:"ssids"`
	Clients      []ClientInfo `json:"clients"`
	Switches     []SwitchInfo `json:"switches,omitempty"`
	WiredClients []ClientInfo `json:"wired_clients,omitempty"`
}

// APInfo describes a single access point.
type APInfo struct {
	Name       string  `json:"name"`
	Model      string  `json:"model"`
	MAC        string  `json:"mac"`
	IP         string  `json:"ip"`
	Version    string  `json:"version"`
	Status     string  `json:"status"`
	NumClients int     `json:"num_clients"`
	Uptime     int64   `json:"uptime"`
	TxBytes    int64   `json:"tx_bytes"`
	RxBytes    int64   `json:"rx_bytes"`
	TxRate     float64 `json:"tx_rate"`
	RxRate     float64 `json:"rx_rate"`
	UplinkMAC  string  `json:"uplink_mac,omitempty"`
}

// SwitchInfo describes a managed switch discovered from a WiFi controller.
type SwitchInfo struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	UplinkMAC string `json:"uplink_mac,omitempty"`
}

// SSIDStat aggregates per-SSID stats.
type SSIDStat struct {
	Name       string  `json:"name"`
	NumClients int     `json:"num_clients"`
	TxBytes    int64   `json:"tx_bytes"`
	RxBytes    int64   `json:"rx_bytes"`
	TxRate     float64 `json:"tx_rate"`
	RxRate     float64 `json:"rx_rate"`
}

// ClientInfo describes a single wireless client.
type ClientInfo struct {
	MAC       string  `json:"mac"`
	Hostname  string  `json:"hostname"`
	IP        string  `json:"ip"`
	SSID      string  `json:"ssid"`
	APMAC     string  `json:"ap_mac"`
	APName    string  `json:"ap_name"`
	SwitchMAC string  `json:"switch_mac,omitempty"`
	Signal    int     `json:"signal"`
	Channel   int     `json:"channel"`
	Radio     string  `json:"radio"`
	TxBytes   int64   `json:"tx_bytes"`
	RxBytes   int64   `json:"rx_bytes"`
	TxRate    float64 `json:"tx_rate"`
	RxRate    float64 `json:"rx_rate"`
	DevCat    int     `json:"dev_cat,omitempty"`
}

// ByteSnap stores TX/RX byte counters for rate delta computation.
type ByteSnap struct {
	Tx int64
	Rx int64
}

// Clamp returns r if r >= 0, otherwise 0.
// Used to handle counter wraps from controller restarts.
func Clamp(r float64) float64 {
	if r < 0 {
		return 0
	}
	return r
}

// ComputeRates calculates TX/RX rates from current and previous byte counters.
// Returns (txRate, rxRate). Clamps negative deltas to 0.
func ComputeRates(curTx, curRx int64, prev ByteSnap, dt float64) (float64, float64) {
	if dt <= 0 {
		return 0, 0
	}
	txRate := Clamp(float64(curTx-prev.Tx) / dt)
	rxRate := Clamp(float64(curRx-prev.Rx) / dt)
	return txRate, rxRate
}

// NormalizedAP is a controller-agnostic AP representation for BuildSummary.
type NormalizedAP struct {
	Name, Model, MAC, IP, Version, Status string
	NumClients                            int
	Uptime, TxBytes, RxBytes              int64
	UplinkMAC                             string
}

// NormalizedSwitch is a controller-agnostic switch representation.
type NormalizedSwitch struct {
	Name, Model, MAC, IP string
	UplinkMAC            string
}

// NormalizedClient is a controller-agnostic client for BuildSummary.
type NormalizedClient struct {
	MAC, Hostname, IP, SSID, APMAC, APName, SwitchMAC string
	Signal, Channel                                   int
	Radio                                             string
	TxBytes, RxBytes                                  int64
	IsWireless                                        bool
	DevCat                                            int
}

// BuildSummary creates a Summary from normalized AP, switch, and client data.
// It handles SSID aggregation, rate computation, and sorting.
func BuildSummary(providerName string, rawAPs []NormalizedAP, rawSwitches []NormalizedSwitch, rawClients []NormalizedClient, dt float64, prevAP, prevSSID, prevCli map[string]ByteSnap) *Summary {
	// Build APs
	var aps []APInfo
	for _, d := range rawAPs {
		ap := APInfo{Name: d.Name, Model: d.Model, MAC: d.MAC, IP: d.IP, Version: d.Version, Status: d.Status, NumClients: d.NumClients, Uptime: d.Uptime, TxBytes: d.TxBytes, RxBytes: d.RxBytes, UplinkMAC: d.UplinkMAC}
		if dt > 0 {
			if prev, ok := prevAP[d.MAC]; ok {
				ap.TxRate, ap.RxRate = ComputeRates(d.TxBytes, d.RxBytes, prev, dt)
			}
		}
		aps = append(aps, ap)
	}
	sort.Slice(aps, func(i, j int) bool { return aps[i].Name < aps[j].Name })

	// SSID aggregation
	type ssidAcc struct {
		count int
		tx    int64
		rx    int64
	}
	ssidMap := make(map[string]*ssidAcc)
	totalWireless := 0
	for _, cl := range rawClients {
		if !cl.IsWireless {
			continue
		}
		totalWireless++
		if cl.SSID != "" {
			a, ok := ssidMap[cl.SSID]
			if !ok {
				a = &ssidAcc{}
				ssidMap[cl.SSID] = a
			}
			a.count++
			a.tx += cl.TxBytes
			a.rx += cl.RxBytes
		}
	}
	var ssids []SSIDStat
	for name, a := range ssidMap {
		s := SSIDStat{Name: name, NumClients: a.count, TxBytes: a.tx, RxBytes: a.rx}
		if dt > 0 {
			if prev, ok := prevSSID[name]; ok {
				s.TxRate, s.RxRate = ComputeRates(a.tx, a.rx, prev, dt)
			}
		}
		ssids = append(ssids, s)
	}
	sort.Slice(ssids, func(i, j int) bool { return ssids[i].NumClients > ssids[j].NumClients })

	// AP name lookup
	apNames := make(map[string]string, len(aps))
	for _, ap := range aps {
		apNames[ap.MAC] = ap.Name
	}

	// Clients
	var clients []ClientInfo
	for _, cl := range rawClients {
		if !cl.IsWireless {
			continue
		}
		apName := cl.APName
		if apName == "" {
			apName = apNames[cl.APMAC]
		}
		ci := ClientInfo{MAC: cl.MAC, Hostname: cl.Hostname, IP: cl.IP, SSID: cl.SSID, APMAC: cl.APMAC, APName: apName, Signal: cl.Signal, Channel: cl.Channel, Radio: cl.Radio, TxBytes: cl.TxBytes, RxBytes: cl.RxBytes, DevCat: cl.DevCat}
		if dt > 0 {
			if prev, ok := prevCli[cl.MAC]; ok {
				ci.TxRate, ci.RxRate = ComputeRates(cl.TxBytes, cl.RxBytes, prev, dt)
			}
		}
		clients = append(clients, ci)
	}
	sort.Slice(clients, func(i, j int) bool {
		return (clients[i].TxBytes + clients[i].RxBytes) > (clients[j].TxBytes + clients[j].RxBytes)
	})

	// Build switches
	var switches []SwitchInfo
	for _, d := range rawSwitches {
		switches = append(switches, SwitchInfo{Name: d.Name, Model: d.Model, MAC: d.MAC, IP: d.IP, UplinkMAC: d.UplinkMAC})
	}

	// Build wired clients with switch association (for topology)
	var wiredClients []ClientInfo
	for _, cl := range rawClients {
		if cl.IsWireless || cl.SwitchMAC == "" {
			continue
		}
		wiredClients = append(wiredClients, ClientInfo{
			MAC:       cl.MAC,
			Hostname:  cl.Hostname,
			IP:        cl.IP,
			SwitchMAC: cl.SwitchMAC,
		})
	}

	return &Summary{ProviderName: providerName, TotalAPs: len(aps), TotalClients: totalWireless, APs: aps, SSIDs: ssids, Clients: clients, Switches: switches, WiredClients: wiredClients}
}

// StoreSnapshots returns prev-maps for the next delta cycle from a Summary.
func StoreSnapshots(sum *Summary) (ap, ssid, cli map[string]ByteSnap) {
	ap = make(map[string]ByteSnap, len(sum.APs))
	for _, a := range sum.APs {
		ap[a.MAC] = ByteSnap{Tx: a.TxBytes, Rx: a.RxBytes}
	}
	ssid = make(map[string]ByteSnap, len(sum.SSIDs))
	for _, s := range sum.SSIDs {
		ssid[s.Name] = ByteSnap{Tx: s.TxBytes, Rx: s.RxBytes}
	}
	cli = make(map[string]ByteSnap, len(sum.Clients))
	for _, c := range sum.Clients {
		cli[c.MAC] = ByteSnap{Tx: c.TxBytes, Rx: c.RxBytes}
	}
	return
}
