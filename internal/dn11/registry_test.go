package dn11

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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

func buildTestTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRegistryTarGzSyncAndHotReload(t *testing.T) {
	v1Tar := buildTestTarGz(t, map[string]string{
		"registry-main/as/4211110101.yml": `
name: BaiMeow
ip:
  - 172.16.10.0/24
ipv6:
  - fd11:10::/48
`,
		"registry-main/as/4211110202.yaml": `
netname: HDU-Net
ip: 172.16.20.0/24
`,
		"registry-main/as/service.yml": `
name: IgnoredServiceWithoutASN
ip:
  - 172.16.250.0/24
`,
		"registry-main/README.md": "# DN11 Registry",
	})

	v2Tar := buildTestTarGz(t, map[string]string{
		"registry-main/as/4211110101.yml": `
name: BaiMeow-V2
ip:
  - 172.16.10.0/24
`,
	})

	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		switch n {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write(v1Tar)
		case 2:
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write(v1Tar)
		default:
			w.Header().Set("ETag", `"v2"`)
			_, _ = w.Write(v2Tar)
		}
	}))
	defer srv.Close()

	reg, err := NewRegistry([]PrefixEntry{
		{CIDR: "172.16.0.0/16", ASN: 4211110000, Owner: "DN11 Backbone", Region: "DN11"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg.StartSync(ctx, SyncOptions{
		URL:      srv.URL,
		Interval: 40 * time.Millisecond,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// Wait for v1 sync to populate in-memory prefixes.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m := reg.Lookup("172.16.10.5"); m != nil && m.ASN == 4211110101 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m := reg.Lookup("172.16.10.5"); m == nil || m.ASN != 4211110101 || m.Owner != "BaiMeow" {
		t.Fatalf("after v1 sync, Lookup(172.16.10.5) = %+v, want ASN=4211110101 Owner=BaiMeow", m)
	}
	if m := reg.Lookup("172.16.20.8"); m == nil || m.ASN != 4211110202 || m.Owner != "HDU-Net" {
		t.Fatalf("after v1 sync, Lookup(172.16.20.8) = %+v, want ASN=4211110202 Owner=HDU-Net", m)
	}
	if m := reg.Lookup("fd11:10::1"); m == nil || m.ASN != 4211110101 {
		t.Fatalf("after v1 sync, Lookup(fd11:10::1) = %+v, want ASN=4211110101", m)
	}
	// Static aggregate fallback still works for unmatched /24s inside 172.16.0.0/16.
	if m := reg.Lookup("172.16.99.1"); m == nil || m.ASN != 4211110000 {
		t.Fatalf("static fallback Lookup(172.16.99.1) = %+v, want ASN=4211110000", m)
	}

	// Wait for v2 hot reload after 304 Not Modified on request #2.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m := reg.Lookup("172.16.10.5"); m != nil && m.Owner == "BaiMeow-V2" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if m := reg.Lookup("172.16.10.5"); m == nil || m.Owner != "BaiMeow-V2" {
		t.Fatalf("after v2 reload, Lookup(172.16.10.5) = %+v, want Owner=BaiMeow-V2", m)
	}
}

func TestParseROAJSON(t *testing.T) {
	raw := []byte(`{"roas":[{"prefix":"172.16.33.0/24","asn":"AS4211110333","owner":"TestROA"}]}`)
	entries, err := parseRegistryPayload(raw)
	if err != nil {
		t.Fatalf("parseRegistryPayload: %v", err)
	}
	if len(entries) != 1 || entries[0].ASN != 4211110333 || entries[0].CIDR != "172.16.33.0/24" || entries[0].Owner != "TestROA" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
