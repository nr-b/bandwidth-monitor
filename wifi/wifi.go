// Package wifi defines the common interface for WiFi controller providers
// (UniFi, Omada, etc.), following the same pattern as the dns package.
package wifi

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
	MAC      string  `json:"mac"`
	Hostname string  `json:"hostname"`
	IP       string  `json:"ip"`
	SSID     string  `json:"ssid"`
	APMAC    string  `json:"ap_mac"`
	APName   string  `json:"ap_name"`
	Signal   int     `json:"signal"`
	Channel  int     `json:"channel"`
	Radio    string  `json:"radio"`
	TxBytes  int64   `json:"tx_bytes"`
	RxBytes  int64   `json:"rx_bytes"`
	TxRate   float64 `json:"tx_rate"`
	RxRate   float64 `json:"rx_rate"`
}
