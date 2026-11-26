package dn11

import (
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/dn-11/11dog/internal/model"
)

// PrefixEntry maps a DN11 IP prefix to its ASN, owner, and region metadata.
type PrefixEntry struct {
	CIDR   string `yaml:"cidr" json:"cidr"`
	ASN    uint32 `yaml:"asn" json:"asn"`
	Owner  string `yaml:"owner" json:"owner"`
	Region string `yaml:"region" json:"region"`
}

type parsedPrefix struct {
	prefix netip.Prefix
	match  model.DN11Match
}

// Registry performs lock-free longest-prefix lookup against in-memory DN11 CIDRs
// and supports atomic hot-reloading from a remote registry.
type Registry struct {
	entries atomic.Pointer[[]parsedPrefix]

	mu     sync.Mutex
	static []parsedPrefix
}

func parseEntries(entries []PrefixEntry, strict bool) ([]parsedPrefix, error) {
	parsed := make([]parsedPrefix, 0, len(entries))
	for _, entry := range entries {
		p, err := netip.ParsePrefix(entry.CIDR)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("invalid dn11 prefix %q: %w", entry.CIDR, err)
			}
			continue
		}
		p = p.Masked()
		parsed = append(parsed, parsedPrefix{
			prefix: p,
			match: model.DN11Match{
				ASN:    entry.ASN,
				Owner:  entry.Owner,
				Region: entry.Region,
				Prefix: p.String(),
			},
		})
	}
	return parsed, nil
}

func sortAndDedupe(primary, fallback []parsedPrefix) []parsedPrefix {
	seen := make(map[netip.Prefix]struct{}, len(primary)+len(fallback))
	merged := make([]parsedPrefix, 0, len(primary)+len(fallback))
	for _, item := range primary {
		if _, ok := seen[item.prefix]; ok {
			continue
		}
		seen[item.prefix] = struct{}{}
		merged = append(merged, item)
	}
	for _, item := range fallback {
		if _, ok := seen[item.prefix]; ok {
			continue
		}
		seen[item.prefix] = struct{}{}
		merged = append(merged, item)
	}
	// Sort most-specific (longest mask bits) first for longest-prefix matching.
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].prefix.Bits() != merged[j].prefix.Bits() {
			return merged[i].prefix.Bits() > merged[j].prefix.Bits()
		}
		return merged[i].prefix.String() < merged[j].prefix.String()
	})
	return merged
}

// NewRegistry builds a prefix lookup registry from statically configured PrefixEntry slices.
func NewRegistry(entries []PrefixEntry) (*Registry, error) {
	parsed, err := parseEntries(entries, true)
	if err != nil {
		return nil, err
	}
	merged := sortAndDedupe(parsed, nil)
	r := &Registry{static: parsed}
	r.entries.Store(&merged)
	return r, nil
}

// DefaultRegistry returns a registry pre-populated with common DN11 private ranges
// when no custom prefixes are supplied.
func DefaultRegistry() *Registry {
	r, _ := NewRegistry([]PrefixEntry{
		{CIDR: "172.16.0.0/16", ASN: 4211110000, Owner: "DN11 Core Backbone", Region: "DN11"},
	})
	return r
}

// UpdateRemote atomically replaces the synced remote prefixes while retaining
// statically configured prefixes as fallback/overrides.
func (r *Registry) UpdateRemote(entries []PrefixEntry) int {
	if r == nil {
		return 0
	}
	remote, _ := parseEntries(entries, false)
	r.mu.Lock()
	merged := sortAndDedupe(remote, r.static)
	r.entries.Store(&merged)
	r.mu.Unlock()
	return len(merged)
}

// Lookup resolves an IP string to the most specific matching DN11 metadata.
func (r *Registry) Lookup(ipStr string) *model.DN11Match {
	if r == nil || ipStr == "" {
		return nil
	}
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return nil
	}
	addr = addr.Unmap()
	ptr := r.entries.Load()
	if ptr == nil {
		return nil
	}
	for _, entry := range *ptr {
		if entry.prefix.Contains(addr) {
			copyMatch := entry.match
			return &copyMatch
		}
	}
	return nil
}

// Len returns the number of currently loaded prefixes.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	ptr := r.entries.Load()
	if ptr == nil {
		return 0
	}
	return len(*ptr)
}
