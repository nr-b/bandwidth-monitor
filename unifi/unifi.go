package unifi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"sync"
	"time"

	"bandwidth-monitor/wifi"
)

type Client struct {
	baseURL    string
	user       string
	pass       string
	site       string
	interval   time.Duration
	httpClient *http.Client
	mu         sync.RWMutex
	summary    *wifi.Summary
	stopCh     chan struct{}

	// API variant detection
	unifiOS   bool   // true = UDM/UDR/CloudKey Gen2+, false = legacy controller
	detected  bool   // true once API variant has been determined
	csrfToken string // X-CSRF-Token for UniFi OS
	loggedIn  bool   // true if we have an active session

	// rate tracking
	lastPoll time.Time
	prevAP   map[string]wifi.ByteSnap // keyed by MAC
	prevSSID map[string]wifi.ByteSnap // keyed by SSID name
	prevCli  map[string]wifi.ByteSnap // keyed by client MAC
}

func New(baseURL, user, pass, site string, pollInterval time.Duration) *Client {
	if site == "" {
		site = "default"
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  baseURL,
		user:     user,
		pass:     pass,
		site:     site,
		interval: pollInterval,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		stopCh: make(chan struct{}),
	}
}

func (c *Client) Run() {
	c.poll()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.poll()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Client) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

func (c *Client) GetSummary() *wifi.Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.summary
}

func (c *Client) Available() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.summary != nil
}

func (c *Client) poll() {
	// Only login if we don't have a session yet
	if !c.loggedIn {
		if err := c.login(); err != nil {
			log.Printf("unifi: login failed: %v", err)
			return
		}
	}
	devices, err := c.fetchDevices()
	if err != nil {
		// If auth error, re-login once and retry
		log.Printf("unifi: fetch devices: %v (re-authenticating)", err)
		c.loggedIn = false
		if err := c.login(); err != nil {
			log.Printf("unifi: re-login failed: %v", err)
			return
		}
		devices, err = c.fetchDevices()
		if err != nil {
			log.Printf("unifi: fetch devices after re-login: %v", err)
			return
		}
	}
	clients, err := c.fetchClients()
	if err != nil {
		log.Printf("unifi: fetch clients: %v", err)
		return
	}

	now := time.Now()
	dt := now.Sub(c.lastPoll).Seconds()
	if c.lastPoll.IsZero() {
		dt = 0
	}

	sum := c.buildSummary(devices, clients, dt)

	// Store current counters for next delta
	newAP := make(map[string]wifi.ByteSnap, len(sum.APs))
	for _, ap := range sum.APs {
		newAP[ap.MAC] = wifi.ByteSnap{Tx: ap.TxBytes, Rx: ap.RxBytes}
	}
	newSSID := make(map[string]wifi.ByteSnap, len(sum.SSIDs))
	for _, s := range sum.SSIDs {
		newSSID[s.Name] = wifi.ByteSnap{Tx: s.TxBytes, Rx: s.RxBytes}
	}
	newCli := make(map[string]wifi.ByteSnap, len(sum.Clients))
	for _, cl := range sum.Clients {
		newCli[cl.MAC] = wifi.ByteSnap{Tx: cl.TxBytes, Rx: cl.RxBytes}
	}

	c.mu.Lock()
	c.summary = sum
	c.prevAP = newAP
	c.prevSSID = newSSID
	c.prevCli = newCli
	c.lastPoll = now
	c.mu.Unlock()
}

func (c *Client) login() error {
	payload, _ := json.Marshal(map[string]string{
		"username": c.user,
		"password": c.pass,
	})

	// Auto-detect API variant on first login
	if !c.detected {
		// Try UniFi OS first (UDM/UDR/CloudKey Gen2+)
		url := c.baseURL + "/api/auth/login"
		resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(payload))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				c.unifiOS = true
				c.detected = true
				c.loggedIn = true
				c.csrfToken = resp.Header.Get("X-CSRF-Token")
				log.Printf("unifi: detected UniFi OS controller")
				return nil
			}
		}
		// Fall back to legacy controller
		url = c.baseURL + "/api/login"
		resp, err = c.httpClient.Post(url, "application/json", bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("POST %s: %w", url, err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("login returned status %d", resp.StatusCode)
		}
		c.unifiOS = false
		c.detected = true
		c.loggedIn = true
		log.Printf("unifi: detected legacy controller")
		return nil
	}

	// Subsequent logins use the detected variant
	url := c.loginURL()
	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login returned status %d", resp.StatusCode)
	}
	if c.unifiOS {
		c.csrfToken = resp.Header.Get("X-CSRF-Token")
	}
	c.loggedIn = true
	return nil
}

func (c *Client) loginURL() string {
	if c.unifiOS {
		return c.baseURL + "/api/auth/login"
	}
	return c.baseURL + "/api/login"
}

func (c *Client) apiPrefix() string {
	if c.unifiOS {
		return c.baseURL + "/proxy/network/api/s/" + c.site
	}
	return c.baseURL + "/api/s/" + c.site
}

type deviceResponse struct {
	Meta struct {
		RC string `json:"rc"`
	} `json:"meta"`
	Data []rawDevice `json:"data"`
}

type rawDevice struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Model   string `json:"model"`
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Version string `json:"version"`
	State   int    `json:"state"`
	NumSta  int    `json:"num_sta"`
	Uptime  int64  `json:"uptime"`
	TxBytes int64  `json:"tx_bytes"`
	RxBytes int64  `json:"rx_bytes"`
}

type clientResponse struct {
	Meta struct {
		RC string `json:"rc"`
	} `json:"meta"`
	Data []rawClient `json:"data"`
}

type rawClient struct {
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	ESSID    string `json:"essid"`
	IsWired  bool   `json:"is_wired"`
	TxBytes  int64  `json:"tx_bytes"`
	RxBytes  int64  `json:"rx_bytes"`
	APMAC    string `json:"ap_mac"`
	Signal   int    `json:"signal"`
	Channel  int    `json:"channel"`
	Radio    string `json:"radio"`
	TxRate   int    `json:"tx_rate"`
	RxRate   int    `json:"rx_rate"`
}

func (c *Client) fetchDevices() ([]rawDevice, error) {
	url := c.apiPrefix() + "/stat/device"
	req, _ := http.NewRequest("GET", url, nil)
	if c.unifiOS && c.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var dr deviceResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return dr.Data, nil
}

func (c *Client) fetchClients() ([]rawClient, error) {
	url := c.apiPrefix() + "/stat/sta"
	req, _ := http.NewRequest("GET", url, nil)
	if c.unifiOS && c.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var cr clientResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return cr.Data, nil
}

func (c *Client) buildSummary(devices []rawDevice, clients []rawClient, dt float64) *wifi.Summary {
	var aps []wifi.APInfo
	for _, d := range devices {
		if d.Type != "uap" {
			continue
		}
		status := "disconnected"
		if d.State == 1 {
			status = "connected"
		}
		ap := wifi.APInfo{
			Name:       d.Name,
			Model:      d.Model,
			MAC:        d.MAC,
			IP:         d.IP,
			Version:    d.Version,
			Status:     status,
			NumClients: d.NumSta,
			Uptime:     d.Uptime,
			TxBytes:    d.TxBytes,
			RxBytes:    d.RxBytes,
		}
		if dt > 0 {
			if prev, ok := c.prevAP[d.MAC]; ok {
				ap.TxRate, ap.RxRate = wifi.ComputeRates(d.TxBytes, d.RxBytes, prev, dt)
			}
		}
		aps = append(aps, ap)
	}
	sort.Slice(aps, func(i, j int) bool { return aps[i].Name < aps[j].Name })

	type ssidAgg struct {
		count   int
		txBytes int64
		rxBytes int64
	}
	ssidMap := make(map[string]*ssidAgg)
	totalWireless := 0
	for _, cl := range clients {
		if cl.IsWired {
			continue
		}
		totalWireless++
		if cl.ESSID != "" {
			a, ok := ssidMap[cl.ESSID]
			if !ok {
				a = &ssidAgg{}
				ssidMap[cl.ESSID] = a
			}
			a.count++
			a.txBytes += cl.TxBytes
			a.rxBytes += cl.RxBytes
		}
	}

	var ssids []wifi.SSIDStat
	for name, a := range ssidMap {
		s := wifi.SSIDStat{Name: name, NumClients: a.count, TxBytes: a.txBytes, RxBytes: a.rxBytes}
		if dt > 0 {
			if prev, ok := c.prevSSID[name]; ok {
				s.TxRate, s.RxRate = wifi.ComputeRates(a.txBytes, a.rxBytes, prev, dt)
			}
		}
		ssids = append(ssids, s)
	}
	sort.Slice(ssids, func(i, j int) bool { return ssids[i].NumClients > ssids[j].NumClients })

	// Build AP MAC → name lookup
	apNames := make(map[string]string)
	for _, ap := range aps {
		apNames[ap.MAC] = ap.Name
	}

	// Build per-client list (wireless only), sorted by total traffic descending
	var clientInfos []wifi.ClientInfo
	for _, cl := range clients {
		if cl.IsWired {
			continue
		}
		ci := wifi.ClientInfo{
			MAC:      cl.MAC,
			Hostname: cl.Hostname,
			IP:       cl.IP,
			SSID:     cl.ESSID,
			APMAC:    cl.APMAC,
			APName:   apNames[cl.APMAC],
			Signal:   cl.Signal,
			Channel:  cl.Channel,
			Radio:    cl.Radio,
			TxBytes:  cl.TxBytes,
			RxBytes:  cl.RxBytes,
		}
		if dt > 0 {
			if prev, ok := c.prevCli[cl.MAC]; ok {
				ci.TxRate, ci.RxRate = wifi.ComputeRates(cl.TxBytes, cl.RxBytes, prev, dt)
			}
		}
		clientInfos = append(clientInfos, ci)
	}
	sort.Slice(clientInfos, func(i, j int) bool {
		return (clientInfos[i].TxBytes + clientInfos[i].RxBytes) >
			(clientInfos[j].TxBytes + clientInfos[j].RxBytes)
	})

	return &wifi.Summary{
		ProviderName: "UniFi",
		TotalAPs:     len(aps),
		TotalClients: totalWireless,
		APs:          aps,
		SSIDs:        ssids,
		Clients:      clientInfos,
	}
}

func (c *Client) String() string {
	variant := "legacy"
	if c.unifiOS {
		variant = "unifi-os"
	}
	return fmt.Sprintf("UniFi[%s/s/%s (%s)]", c.baseURL, c.site, variant)
}
