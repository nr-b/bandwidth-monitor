package geoip

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"bandwidth-monitor/poller"

	"github.com/oschwald/maxminddb-golang"
)

// Result holds the geo + ASN information for a single IP.
type Result struct {
	Country     string  `json:"country"`      // ISO 3166-1 alpha-2
	CountryName string  `json:"country_name"` // English name
	City        string  `json:"city,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	ASN         uint    `json:"asn,omitempty"`
	ASOrg       string  `json:"as_org,omitempty"`
}

// DB wraps the MaxMind MMDB readers with a lookup cache.
type DB struct {
	country *maxminddb.Reader
	asn     *maxminddb.Reader
	mu      sync.RWMutex
	cache   map[string]*Result
	poller.Runner
}

// cityRecord is the minimal struct for MMDB city/country lookups.
type cityRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

// asnRecord is the minimal struct for MMDB ASN lookups.
type asnRecord struct {
	ASN uint   `maxminddb:"autonomous_system_number"`
	Org string `maxminddb:"autonomous_system_organization"`
}

// Open loads the MMDB files. Either or both paths may be empty — lookups
// will gracefully return partial results.
func Open(countryPath, asnPath string) (*DB, error) {
	db := &DB{
		cache: make(map[string]*Result, 4096),
	}
	db.Runner.Init()

	if countryPath != "" {
		if _, err := os.Stat(countryPath); err == nil {
			r, err := maxminddb.Open(countryPath)
			if err != nil {
				return nil, fmt.Errorf("geoip: open country db: %w", err)
			}
			db.country = r
		}
	}

	if asnPath != "" {
		if _, err := os.Stat(asnPath); err == nil {
			r, err := maxminddb.Open(asnPath)
			if err != nil {
				return nil, fmt.Errorf("geoip: open ASN db: %w", err)
			}
			db.asn = r
		}
	}

	// Start periodic cache pruning to bound memory usage.
	go db.pruneLoop()

	return db, nil
}

const (
	geoCachePruneInterval = 1 * time.Hour
	geoCacheMaxSize       = 100_000 // hard cap — clear entire cache if exceeded
)

// pruneLoop periodically clears the GeoIP cache to prevent unbounded growth.
// MMDB lookups are fast (~1µs) so rebuilding the cache is cheap.
func (db *DB) pruneLoop() {
	db.Runner.Run(geoCachePruneInterval, func() {
		db.mu.Lock()
		if len(db.cache) > geoCacheMaxSize {
			db.cache = make(map[string]*Result, 4096)
		}
		db.mu.Unlock()
	})
}

// Close releases the database readers and stops the pruning goroutine.
func (db *DB) Close() {
	db.Runner.Stop()
	if db.country != nil {
		db.country.Close()
	}
	if db.asn != nil {
		db.asn.Close()
	}
}

// Available returns true if at least one database was loaded.
func (db *DB) Available() bool {
	return db.country != nil || db.asn != nil
}

// Lookup returns geo information for an IP address. Results are cached.
func (db *DB) Lookup(ipStr string) *Result {
	if db == nil || !db.Available() {
		return nil
	}

	db.mu.RLock()
	if r, ok := db.cache[ipStr]; ok {
		db.mu.RUnlock()
		return r
	}
	db.mu.RUnlock()

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil
	}

	r := &Result{}

	if db.country != nil {
		var rec cityRecord
		if err := db.country.Lookup(ip, &rec); err == nil {
			r.Country = rec.Country.ISOCode
			r.CountryName = rec.Country.Names["en"]
			if name, ok := rec.City.Names["en"]; ok {
				r.City = name
			}
			r.Latitude = rec.Location.Latitude
			r.Longitude = rec.Location.Longitude
		}
	}

	if db.asn != nil {
		var rec asnRecord
		if err := db.asn.Lookup(ip, &rec); err == nil {
			r.ASN = rec.ASN
			r.ASOrg = rec.Org
		}
	}

	db.mu.Lock()
	db.cache[ipStr] = r
	db.mu.Unlock()

	return r
}

// GeoFields is implemented by any struct that has geo fields to be enriched.
type GeoFields interface {
	SetGeo(country, countryName, city string, lat, lon float64, asn uint, asOrg string)
}

// Enrich populates geo fields on a target struct from the database.
func (db *DB) Enrich(ipStr string, target GeoFields) {
	if db == nil || !db.Available() {
		return
	}
	if geo := db.Lookup(ipStr); geo != nil {
		target.SetGeo(geo.Country, geo.CountryName, geo.City, geo.Latitude, geo.Longitude, geo.ASN, geo.ASOrg)
	}
}
