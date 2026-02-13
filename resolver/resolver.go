// Package resolver provides a shared reverse-DNS resolver with TTL-based
// caching and bounded concurrency.  Both the conntrack and talkers packages
// (and anything else that needs IP-to-hostname lookups) should use a single
// shared instance so that cache entries are not duplicated and TTL expiry
// is handled consistently.
package resolver

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// Default tuning knobs.
const (
	DefaultTTL        = 5 * time.Minute
	DefaultNegTTL     = 1 * time.Minute
	DefaultTimeout    = 500 * time.Millisecond
	DefaultConcurrent = 16
)

// cacheEntry stores a resolved hostname with an expiry timestamp.
type cacheEntry struct {
	hostname string
	expires  time.Time
}

// Resolver performs reverse-DNS lookups with a TTL-based cache and bounded
// concurrency.
type Resolver struct {
	mu      sync.RWMutex
	cache   map[string]cacheEntry
	sem     chan struct{} // limits concurrent lookups
	ttl     time.Duration
	negTTL  time.Duration
	timeout time.Duration
}

// New creates a Resolver with sensible defaults.
func New() *Resolver {
	return &Resolver{
		cache:   make(map[string]cacheEntry, 256),
		sem:     make(chan struct{}, DefaultConcurrent),
		ttl:     DefaultTTL,
		negTTL:  DefaultNegTTL,
		timeout: DefaultTimeout,
	}
}

// LookupAddr performs a synchronous reverse-DNS lookup for ip, returning the
// resolved hostname or "" on failure.  Results are cached with TTL.
func (r *Resolver) LookupAddr(ip string) string {
	// Fast path: check cache under read lock.
	now := time.Now()
	r.mu.RLock()
	entry, ok := r.cache[ip]
	r.mu.RUnlock()
	if ok && now.Before(entry.expires) {
		return entry.hostname
	}

	return r.resolve(ip)
}

// LookupAddrFresh always performs a fresh reverse-DNS lookup, bypassing the
// cache.  The result is still stored in the cache so subsequent LookupAddr
// calls benefit from it.
func (r *Resolver) LookupAddrFresh(ip string) string {
	return r.resolve(ip)
}

// resolve does the actual DNS lookup and caches the result.
func (r *Resolver) resolve(ip string) string {
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	cancel()

	var hostname string
	var cacheTTL time.Duration
	if err == nil && len(names) > 0 {
		hostname = strings.TrimSuffix(names[0], ".")
		cacheTTL = r.ttl
	} else {
		hostname = ""
		cacheTTL = r.negTTL
	}

	r.mu.Lock()
	r.cache[ip] = cacheEntry{hostname: hostname, expires: now.Add(cacheTTL)}
	r.mu.Unlock()

	return hostname
}

// LookupAddrAsync returns the cached hostname for ip immediately.  If there
// is no cached result (or it has expired), it returns the raw IP and kicks off
// a background lookup so the next call will have the resolved name.
//
// This is the preferred method for hot paths (e.g. per-second SSE payloads)
// where blocking on DNS is undesirable.
func (r *Resolver) LookupAddrAsync(ip string) string {
	now := time.Now()
	r.mu.RLock()
	entry, ok := r.cache[ip]
	r.mu.RUnlock()
	if ok && now.Before(entry.expires) {
		if entry.hostname != "" {
			return entry.hostname
		}
		return ip // negative cache hit — don't re-trigger
	}

	// Store the IP as a placeholder so concurrent calls don't all trigger
	// lookups for the same address.
	r.mu.Lock()
	// Double-check after write lock.
	if entry, ok := r.cache[ip]; ok && now.Before(entry.expires) {
		r.mu.Unlock()
		if entry.hostname != "" {
			return entry.hostname
		}
		return ip
	}
	r.cache[ip] = cacheEntry{hostname: "", expires: now.Add(r.negTTL)}
	r.mu.Unlock()

	// Fire-and-forget with bounded concurrency.
	select {
	case r.sem <- struct{}{}:
		go func() {
			defer func() { <-r.sem }()

			ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
			names, err := net.DefaultResolver.LookupAddr(ctx, ip)
			cancel()

			if err != nil || len(names) == 0 {
				return // negative entry already cached above
			}

			resolved := strings.TrimSuffix(names[0], ".")
			r.mu.Lock()
			r.cache[ip] = cacheEntry{hostname: resolved, expires: time.Now().Add(r.ttl)}
			r.mu.Unlock()
		}()
	default:
		// Semaphore full — skip, will retry on next call.
	}

	return ip
}
