package dn11

import "testing"

func TestRegistryLongestPrefixMatch(t *testing.T) {
	reg, err := NewRegistry([]PrefixEntry{
		{CIDR: "172.16.0.0/16", ASN: 4211110000, Owner: "DN11 Aggregate", Region: "Global"},
		{CIDR: "172.16.10.0/24", ASN: 4211110101, Owner: "Node Alpha", Region: "HK"},
		{CIDR: "172.16.10.128/25", ASN: 4211110202, Owner: "Node Beta", Region: "JP"},
		{CIDR: "fd11:10::/32", ASN: 4211110303, Owner: "Node v6", Region: "SG"},
	})
	if err != nil {
		t.Fatalf("unexpected error building registry: %v", err)
	}

	tests := []struct {
		ip        string
		wantASN   uint32
		wantOwner string
		wantNil   bool
	}{
		{ip: "172.16.10.130", wantASN: 4211110202, wantOwner: "Node Beta"},
		{ip: "172.16.10.5", wantASN: 4211110101, wantOwner: "Node Alpha"},
		{ip: "172.16.99.1", wantASN: 4211110000, wantOwner: "DN11 Aggregate"},
		{ip: "fd11:10::1", wantASN: 4211110303, wantOwner: "Node v6"},
		{ip: "8.8.8.8", wantNil: true},
		{ip: "not-an-ip", wantNil: true},
	}

	for _, tc := range tests {
		got := reg.Lookup(tc.ip)
		if tc.wantNil {
			if got != nil {
				t.Errorf("Lookup(%q) = %+v, want nil", tc.ip, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("Lookup(%q) = nil, want ASN %d", tc.ip, tc.wantASN)
		}
		if got.ASN != tc.wantASN || got.Owner != tc.wantOwner {
			t.Errorf("Lookup(%q) = %+v, want ASN=%d Owner=%q", tc.ip, got, tc.wantASN, tc.wantOwner)
		}
	}
}
