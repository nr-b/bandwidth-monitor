// Package omada implements a wifi.Provider for TP-Link Omada controllers
// (hardware OC200/OC300 or software controller).
package omada

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strings"
	"sync"
	"time"

	"bandwidth-monitor/wifi"
)

// Client polls an Omada controller for WiFi stats.
type Client struct {
	baseURL   string
	user      string
	pass      string
	siteName  string
	interval  time.Duration
	httpC     *http.Client
	mu        sync.RWMutex
	summary   *wifi.Summary
	stopCh    chan struct{}
	token     string
	omadaCID  string
	siteID    string
	loggedIn  bool
	csrfToken string
	lastPoll  time.Time
	prevAP    map[string]byteSnap
	prevCli   map[string]byteSnap
	prevSSID  map[string]byteSnap
}

type byteSnap struct{ tx, rx int64 }

// New creates an Omada controller client.
func New(baseURL, user, pass, siteName string, pollInterval time.Duration) *Client {
	if siteName == "" {
		siteName = "Default"
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		user:     user,
		pass:     pass,
		siteName: siteName,
		interval: pollInterval,
		httpC: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		stopCh: make(chan struct{}),
	}
}

// Run starts the polling loop. Call in a goroutine.
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

// Stop terminates the polling loop.
func (c *Client) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
}

// GetSummary returns the latest WiFi summary (nil if no data yet).
func (c *Client) GetSummary() *wifi.Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.summary
}

// Available returns true if at least one poll has succeeded.
func (c *Client) Available() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.summary != nil
}

func (c *Client) poll() {
	if !c.loggedIn {
		if err := c.login(); err != nil {
			log.Printf("omada: login failed: %v", err)
			return
		}
	}
	devices, err := c.fetchDevices()
	if err != nil {
		log.Printf("omada: fetch devices: %v (re-authenticating)", err)
		c.loggedIn = false
		if err := c.login(); err != nil {
			log.Printf("omada: re-login failed: %v", err)
			return
		}
		devices, err = c.fetchDevices()
		if err != nil {
			log.Printf("omada: fetch devices after re-login: %v", err)
			return
		}
	}
	clients, err := c.fetchClients()
	if err != nil {
		log.Printf("omada: fetch clients: %v", err)
		return
	}

	now := time.Now()
	dt := now.Sub(c.lastPoll).Seconds()
	if c.lastPoll.IsZero() {
		dt = 0
	}
	sum := c.buildSummary(devices, clients, dt)

	newAP := make(map[string]byteSnap, len(sum.APs))
	for _, ap := range sum.APs {
		newAP[ap.MAC] = byteSnap{tx: ap.TxBytes, rx: ap.RxBytes}
	}
	newSSID := make(map[string]byteSnap, len(sum.SSIDs))
	for _, s := range sum.SSIDs {
		newSSID[s.Name] = byteSnap{tx: s.TxBytes, rx: s.RxBytes}
	}
	newCli := make(map[string]byteSnap, len(sum.Clients))
	for _, cl := range sum.Clients {
		newCli[cl.MAC] = byteSnap{tx: cl.TxBytes, rx: cl.RxBytes}
	}

	c.mu.Lock()
	c.summary = sum
	c.prevAP = newAP
	c.prevSSID = newSSID
	c.prevCli = newCli
	c.lastPoll = now
	c.mu.Unlock()
}

// -- Omada API types --

type apiResult struct {
	ErrorCode int             `json:"errorCode"`
	Msg       string          `json:"msg"`
	Result    json.RawMessage `json:"result"`
}

type loginResult struct {
	Token string `json:"token"`
}

type controllerInfo struct {
	OmadaCID string `json:"omadacId"`
}

type siteEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type siteList struct {
	Data []siteEntry `json:"data"`
}

type rawDevice struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	Version   string `json:"firmwareVersion"`
	Status    int    `json:"status"`
	ClientNum int    `json:"clientNum"`
	Uptime    int64  `json:"uptimeLong"`
	TxBytes   int64  `json:"tx"`
	RxBytes   int64  `json:"rx"`
}

type deviceList struct {
	Data []rawDevice `json:"data"`
}

type rawClient struct {
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Hostname string `json:"hostName"`
	IP       string `json:"ip"`
	SSID     string `json:"ssid"`
	APMAC    string `json:"apMac"`
	APName   string `json:"apName"`
	SignalDB int    `json:"signalLevel"`
	Channel  int    `json:"channel"`
	RadioID  int    `json:"radioId"`
	TxBytes  int64  `json:"trafficUp"`
	RxBytes  int64  `json:"trafficDown"`
	Wireless bool   `json:"wireless"`
}

type clientList struct {
	Data []rawClient `json:"data"`
}

// -- API calls --

func (c *Client) login() error {
	info, err := c.getJSON("/api/info", nil)
	if err != nil {
		return fmt.Errorf("get controller info: %w", err)
	}
	var ci controllerInfo
	if err := json.Unmarshal(info, &ci); err != nil {
		return fmt.Errorf("parse controller info: %w", err)
	}
	c.omadaCID = ci.OmadaCID

	payload, _ := json.Marshal(map[string]string{
		"username": c.user,
		"password": c.pass,
	})
	body, err := c.postJSON(fmt.Sprintf("/%s/api/v2/login", c.omadaCID), payload)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	var lr loginResult
	if err := json.Unmarshal(body, &lr); err != nil {
		return fmt.Errorf("parse login: %w", err)
	}
	c.token = lr.Token
	c.loggedIn = true
	log.Printf("omada: logged in to controller %s", c.omadaCID)

	if err := c.resolveSite(); err != nil {
		return fmt.Errorf("resolve site: %w", err)
	}
	return nil
}

func (c *Client) resolveSite() error {
	body, err := c.getJSON(fmt.Sprintf("/%s/api/v2/sites?currentPage=1&currentPageSize=100", c.omadaCID), nil)
	if err != nil {
		return err
	}
	var sl siteList
	if err := json.Unmarshal(body, &sl); err != nil {
		return fmt.Errorf("parse sites: %w", err)
	}
	for _, s := range sl.Data {
		if strings.EqualFold(s.Name, c.siteName) {
			c.siteID = s.ID
			log.Printf("omada: site %q resolved to ID %s", c.siteName, c.siteID)
			return nil
		}
	}
	return fmt.Errorf("site %q not found", c.siteName)
}

func (c *Client) sitePrefix() string {
	return fmt.Sprintf("/%s/api/v2/sites/%s", c.omadaCID, c.siteID)
}

func (c *Client) fetchDevices() ([]rawDevice, error) {
	body, err := c.getJSON(c.sitePrefix()+"/devices?currentPage=1&currentPageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	var dl deviceList
	if err := json.Unmarshal(body, &dl); err != nil {
		return nil, fmt.Errorf("parse devices: %w", err)
	}
	return dl.Data, nil
}

func (c *Client) fetchClients() ([]rawClient, error) {
	body, err := c.getJSON(c.sitePrefix()+"/clients?currentPage=1&currentPageSize=5000&filters.wireless=true", nil)
	if err != nil {
		return nil, err
	}
	var cl clientList
	if err := json.Unmarshal(body, &cl); err != nil {
		return nil, fmt.Errorf("parse clients: %w", err)
	}
	return cl.Data, nil
}

// -- HTTP helpers --

func (c *Client) getJSON(path string, headers map[string]string) (json.RawMessage, error) {
	url := c.baseURL + path
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, string(b))
	}
	var ar apiResult
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if ar.ErrorCode != 0 {
		return nil, fmt.Errorf("API error %d: %s", ar.ErrorCode, ar.Msg)
	}
	return ar.Result, nil
}

func (c *Client) postJSON(path string, payload []byte) (json.RawMessage, error) {
	url := c.baseURL + path
	req, err := http.NewRequest("POST", url, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)
	resp, err := c.httpC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, string(b))
	}
	if ct := resp.Header.Get("Csrf-Token"); ct != "" {
		c.csrfToken = ct
	}
	var ar apiResult
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if ar.ErrorCode != 0 {
		return nil, fmt.Errorf("API error %d: %s", ar.ErrorCode, ar.Msg)
	}
	return ar.Result, nil
}

func (c *Client) setHeaders(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "AccessToken="+c.token)
	}
	if c.csrfToken != "" {
		req.Header.Set("Csrf-Token", c.csrfToken)
	}
}

// -- Summary building --

func (c *Client) buildSummary(devices []rawDevice, clients []rawClient, dt float64) *wifi.Summary {
	var aps []wifi.APInfo
	for _, d := range devices {
		if d.Type != "ap" {
			continue
		}
		status := "disconnected"
		if d.Status == 14 {
			status = "connected"
		}
		ap := wifi.APInfo{
			Name: d.Name, Model: d.Model, MAC: d.MAC, IP: d.IP,
			Version: d.Version, Status: status, NumClients: d.ClientNum,
			Uptime: d.Uptime, TxBytes: d.TxBytes, RxBytes: d.RxBytes,
		}
		if dt > 0 {
			if prev, ok := c.prevAP[d.MAC]; ok {
				ap.TxRate = clamp(float64(d.TxBytes-prev.tx) / dt)
				ap.RxRate = clamp(float64(d.RxBytes-prev.rx) / dt)
			}
		}
		aps = append(aps, ap)
	}
	sort.Slice(aps, func(i, j int) bool { return aps[i].Name < aps[j].Name })

	type ssidAcc struct {
		n  int
		tx int64
		rx int64
	}
	sm := make(map[string]*ssidAcc)
	tw := 0
	for _, cl := range clients {
		if !cl.Wireless {
			continue
		}
		tw++
		if cl.SSID != "" {
			a, ok := sm[cl.SSID]
			if !ok {
				a = &ssidAcc{}
				sm[cl.SSID] = a
			}
			a.n++
			a.tx += cl.TxBytes
			a.rx += cl.RxBytes
		}
	}

	var ssids []wifi.SSIDStat
	for name, a := range sm {
		s := wifi.SSIDStat{Name: name, NumClients: a.n, TxBytes: a.tx, RxBytes: a.rx}
		if dt > 0 {
			if prev, ok := c.prevSSID[name]; ok {
				s.TxRate = clamp(float64(a.tx-prev.tx) / dt)
				s.RxRate = clamp(float64(a.rx-prev.rx) / dt)
			}
		}
		ssids = append(ssids, s)
	}
	sort.Slice(ssids, func(i, j int) bool { return ssids[i].NumClients > ssids[j].NumClients })

	apNames := make(map[string]string, len(aps))
	for _, ap := range aps {
		apNames[ap.MAC] = ap.Name
	}

	var cis []wifi.ClientInfo
	for _, cl := range clients {
		if !cl.Wireless {
			continue
		}
		hn := cl.Hostname
		if hn == "" {
			hn = cl.Name
		}
		ci := wifi.ClientInfo{
			MAC: cl.MAC, Hostname: hn, IP: cl.IP, SSID: cl.SSID,
			APMAC: cl.APMAC, APName: cl.APName, Signal: cl.SignalDB,
			Channel: cl.Channel, Radio: radioName(cl.RadioID),
			TxBytes: cl.TxBytes, RxBytes: cl.RxBytes,
		}
		if dt > 0 {
			if prev, ok := c.prevCli[cl.MAC]; ok {
				ci.TxRate = clamp(float64(cl.TxBytes-prev.tx) / dt)
				ci.RxRate = clamp(float64(cl.RxBytes-prev.rx) / dt)
			}
		}
		cis = append(cis, ci)
	}
	sort.Slice(cis, func(i, j int) bool {
		return (cis[i].TxBytes + cis[i].RxBytes) > (cis[j].TxBytes + cis[j].RxBytes)
	})

	return &wifi.Summary{
		ProviderName: "Omada",
		TotalAPs:     len(aps),
		TotalClients: tw,
		APs:          aps,
		SSIDs:        ssids,
		Clients:      cis,
	}
}

func radioName(id int) string {
	switch id {
	case 0:
		return "2.4 GHz"
	case 1:
		return "5 GHz"
	case 2:
		return "6 GHz"
	default:
		return ""
	}
}

func clamp(r float64) float64 {
	if r < 0 {
		return 0
	}
	return r
}

// String returns a debug representation.
func (c *Client) String() string {
	return fmt.Sprintf("Omada[%s/site=%s]", c.baseURL, c.siteName)
}
