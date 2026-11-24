package dn11

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultRegistryURL is the official DN11 GitHub registry tarball URL (tracks default branch without API rate limits).
const DefaultRegistryURL = "https://github.com/dn-11/registry/archive/HEAD.tar.gz"

// SyncOptions configures periodic in-memory synchronization from a remote DN11 registry.
type SyncOptions struct {
	URL        string
	Interval   time.Duration
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// StartSync launches a background loop that fetches the DN11 registry immediately
// and refreshes it periodically in memory until ctx is cancelled.
func (r *Registry) StartSync(ctx context.Context, opts SyncOptions) {
	if r == nil || strings.TrimSpace(opts.URL) == "" {
		return
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	go func() {
		var etag string
		syncOnce := func() {
			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			entries, newETag, notModified, err := fetchRegistry(reqCtx, client, opts.URL, etag)
			if err != nil {
				logger.Warn("failed to sync DN11 registry; keeping existing in-memory prefixes",
					"url", opts.URL,
					"error", err,
					"cached_prefixes", r.Len(),
				)
				return
			}
			if notModified {
				return
			}
			if len(entries) == 0 {
				logger.Warn("synced DN11 registry returned 0 prefixes; keeping existing cache", "url", opts.URL)
				return
			}
			etag = newETag
			total := r.UpdateRemote(entries)
			logger.Info("synced DN11 registry into memory",
				"url", opts.URL,
				"synced_prefixes", len(entries),
				"total_prefixes", total,
			)
		}

		syncOnce()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncOnce()
			}
		}
	}()
}

// SyncOnce fetches the remote registry once and updates r in memory.
func (r *Registry) SyncOnce(ctx context.Context, client *http.Client, rawURL string) (int, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	entries, _, _, err := fetchRegistry(ctx, client, rawURL, "")
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("registry %q contained no valid prefixes", rawURL)
	}
	return r.UpdateRemote(entries), nil
}

func fetchRegistry(ctx context.Context, client *http.Client, rawURL, etag string) ([]PrefixEntry, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "11dog-server/dn11-registry-sync")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	// Limit read to 32 MiB to protect server memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", false, fmt.Errorf("read response: %w", err)
	}

	entries, err := parseRegistryPayload(body)
	if err != nil {
		return nil, "", false, err
	}
	return entries, resp.Header.Get("ETag"), false, nil
}

func parseRegistryPayload(data []byte) ([]PrefixEntry, error) {
	// Gzip magic bytes: 0x1f 0x8b -> tar.gz archive (e.g. GitHub repository archive).
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return parseTarGzRegistry(data)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}

	// JSON array of PrefixEntry or StayRTR/ROA JSON object.
	if trimmed[0] == '[' {
		var list []PrefixEntry
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("parse prefix JSON array: %w", err)
		}
		return list, nil
	}
	if trimmed[0] == '{' {
		var roaDoc struct {
			ROAs []struct {
				Prefix string `json:"prefix"`
				ASN    any    `json:"asn"`
				Owner  string `json:"owner"`
				Region string `json:"region"`
			} `json:"roas"`
		}
		if err := json.Unmarshal(trimmed, &roaDoc); err == nil && len(roaDoc.ROAs) > 0 {
			out := make([]PrefixEntry, 0, len(roaDoc.ROAs))
			for _, item := range roaDoc.ROAs {
				asn := parseAnyASN(item.ASN)
				region := strings.TrimSpace(item.Region)
				if region == "" {
					region = "DN11"
				}
				owner := strings.TrimSpace(item.Owner)
				if owner == "" && asn > 0 {
					owner = fmt.Sprintf("AS%d", asn)
				}
				out = append(out, PrefixEntry{
					CIDR:   strings.TrimSpace(item.Prefix),
					ASN:    asn,
					Owner:  owner,
					Region: region,
				})
			}
			return out, nil
		}
	}

	// Fallback: YAML list of PrefixEntry.
	var yamlList []PrefixEntry
	if err := yaml.Unmarshal(trimmed, &yamlList); err == nil && len(yamlList) > 0 {
		return yamlList, nil
	}
	return nil, fmt.Errorf("unrecognized DN11 registry payload format")
}

// stringOrSlice unmarshals a YAML field that may be either a single string or a list of strings.
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		if v := strings.TrimSpace(single); v != "" {
			*s = []string{v}
		}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		out := make([]string, 0, len(list))
		for _, item := range list {
			if v := strings.TrimSpace(item); v != "" {
				out = append(out, v)
			}
		}
		*s = out
		return nil
	default:
		return nil
	}
}

type registryASFile struct {
	ASN     any           `yaml:"asn"`
	Name    string        `yaml:"name"`
	Netname string        `yaml:"netname"`
	Owner   string        `yaml:"owner"`
	Region  string        `yaml:"region"`
	IP      stringOrSlice `yaml:"ip"`
	IPv4    stringOrSlice `yaml:"ipv4"`
	IPv6    stringOrSlice `yaml:"ipv6"`
	IP6     stringOrSlice `yaml:"ip6"`
}

func parseTarGzRegistry(data []byte) ([]PrefixEntry, error) {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var out []PrefixEntry

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Match "<repo-root>/as/<asn>.yml" or "as/<asn>.yaml".
		cleanName := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		parts := strings.Split(cleanName, "/")
		if len(parts) < 2 {
			continue
		}
		dir := parts[len(parts)-2]
		base := parts[len(parts)-1]
		if dir != "as" {
			continue
		}
		lowerBase := strings.ToLower(base)
		var stem string
		switch {
		case strings.HasSuffix(lowerBase, ".yml"):
			stem = base[:len(base)-4]
		case strings.HasSuffix(lowerBase, ".yaml"):
			stem = base[:len(base)-5]
		default:
			continue
		}

		content, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			continue
		}

		var doc registryASFile
		if err := yaml.Unmarshal(content, &doc); err != nil {
			continue
		}

		asn := parseASNString(stem)
		if asn == 0 {
			asn = parseAnyASN(doc.ASN)
		}
		if asn == 0 {
			continue
		}

		owner := firstNonEmpty(doc.Name, doc.Netname, doc.Owner, fmt.Sprintf("AS%d", asn))
		region := firstNonEmpty(doc.Region, "DN11")

		cidrs := make([]string, 0, len(doc.IP)+len(doc.IPv4)+len(doc.IPv6)+len(doc.IP6))
		cidrs = append(cidrs, doc.IP...)
		cidrs = append(cidrs, doc.IPv4...)
		cidrs = append(cidrs, doc.IPv6...)
		cidrs = append(cidrs, doc.IP6...)

		for _, cidr := range cidrs {
			out = append(out, PrefixEntry{
				CIDR:   cidr,
				ASN:    asn,
				Owner:  owner,
				Region: region,
			})
		}
	}

	return out, nil
}

func parseAnyASN(v any) uint32 {
	switch x := v.(type) {
	case int:
		if x > 0 {
			return uint32(x)
		}
	case int64:
		if x > 0 {
			return uint32(x)
		}
	case uint64:
		return uint32(x)
	case float64:
		if x > 0 {
			return uint32(x)
		}
	case string:
		return parseASNString(x)
	}
	return 0
}

func parseASNString(s string) uint32 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "AS")
	s = strings.TrimPrefix(s, "as")
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
