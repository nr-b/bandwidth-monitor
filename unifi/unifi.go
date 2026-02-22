package unifi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"bandwidth-monitor/httputil"
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
	return &Client{
		baseURL:    baseURL,
		user:       user,
		pass:       pass,
		site:       site,
		interval:   pollInterval,
		httpClient: httputil.NewInsecureClient(15 * time.Second),
		stopCh:     make(chan struct{}),
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

	sum := wifi.BuildSummary("UniFi", c.normalizeAPs(devices), c.normalizeClients(clients), dt, c.prevAP, c.prevSSID, c.prevCli)

	// Store current counters for next delta
	newAP, newSSID, newCli := wifi.StoreSnapshots(sum)

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

func (c *Client) normalizeAPs(devices []rawDevice) []wifi.NormalizedAP {
	var aps []wifi.NormalizedAP
	for _, d := range devices {
		if d.Type != "uap" {
			continue
		}
		status := "disconnected"
		if d.State == 1 {
			status = "connected"
		}
		aps = append(aps, wifi.NormalizedAP{
			Name: d.Name, Model: d.Model, MAC: d.MAC, IP: d.IP,
			Version: d.Version, Status: status, NumClients: d.NumSta,
			Uptime: d.Uptime, TxBytes: d.TxBytes, RxBytes: d.RxBytes,
		})
	}
	return aps
}

func (c *Client) normalizeClients(clients []rawClient) []wifi.NormalizedClient {
	var ncs []wifi.NormalizedClient
	for _, cl := range clients {
		ncs = append(ncs, wifi.NormalizedClient{
			MAC: cl.MAC, Hostname: cl.Hostname, IP: cl.IP, SSID: cl.ESSID,
			APMAC: cl.APMAC, Signal: cl.Signal, Channel: cl.Channel,
			Radio: cl.Radio, TxBytes: cl.TxBytes, RxBytes: cl.RxBytes,
			IsWireless: !cl.IsWired,
		})
	}
	return ncs
}

func (c *Client) String() string {
	variant := "legacy"
	if c.unifiOS {
		variant = "unifi-os"
	}
	return fmt.Sprintf("UniFi[%s/s/%s (%s)]", c.baseURL, c.site, variant)
}
