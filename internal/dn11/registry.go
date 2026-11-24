package dn11

import (
	"fmt"
	"net/netip"
	"sort"

	"github.com/11dog/11dog/internal/model"
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

// Registry performs longest-prefix lookup against configured DN11 CIDRs.
type Registry struct {
	entries []parsedPrefix
}

// NewRegistry builds a prefix lookup registry from configured PrefixEntry slices.
func NewRegistry(entries []PrefixEntry) (*Registry, error) {
	parsed := make([]parsedPrefix, 0, len(entries))
	for _, entry := range entries {
		p, err := netip.ParsePrefix(entry.CIDR)
		if err != nil {
			return nil, fmt.Errorf("invalid dn11 prefix %q: %w", entry.CIDR, err)
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
	// Sort most-specific (longest mask bits) first for longest-prefix matching.
	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].prefix.Bits() > parsed[j].prefix.Bits()
	})
	return &Registry{entries: parsed}, nil
}

// DefaultRegistry returns a registry pre-populated with common DN11 private ranges
// when no custom prefixes are supplied.
func DefaultRegistry() *Registry {
	r, _ := NewRegistry([]PrefixEntry{
		{CIDR: "172.16.0.0/16", ASN: 4211110000, Owner: "DN11 Core Backbone", Region: "DN11"},
		{CIDR: "fd00::/48", ASN: 4211110000, Owner: "DN11 IPv6 Backbone", Region: "DN11"},
	})
	return r
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
	for _, entry := range r.entries {
		if entry.prefix.Contains(addr) {
			copyMatch := entry.match
			return &copyMatch
		}
	}
	return nil
}

// Len returns the number of registered prefixes.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.entries)
}
