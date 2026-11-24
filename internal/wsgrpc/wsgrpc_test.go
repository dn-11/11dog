package wsgrpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeWSURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"127.0.0.1:9090", "", true},
		{"grpc.example.com:443", "", true},
		{"ws://127.0.0.1:8080", "ws://127.0.0.1:8080" + DefaultPath, false},
		{"ws://127.0.0.1:8080/", "ws://127.0.0.1:8080" + DefaultPath, false},
		{"wss://11dog.example.com", "wss://11dog.example.com" + DefaultPath, false},
		{"wss://11dog.example.com/custom/ws", "wss://11dog.example.com/custom/ws", false},
		{"http://127.0.0.1:8080", "ws://127.0.0.1:8080" + DefaultPath, false},
		{"https://11dog.example.com/api/v1/agent/ws", "wss://11dog.example.com/api/v1/agent/ws", false},
	}

	for _, tc := range cases {
		got, err := NormalizeWSURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeWSURL(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeWSURL(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeWSURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestListenerNonUpgradeRejected(t *testing.T) {
	lis := NewListener("test")
	defer lis.Close()

	req := httptest.NewRequest(http.MethodGet, DefaultPath, nil)
	rec := httptest.NewRecorder()
	lis.ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUpgradeRequired)
	}
}

func TestWSConnEchoPlainAndTLS(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "ws"
		if useTLS {
			name = "wss"
		}
		t.Run(name, func(t *testing.T) {
			lis := NewListener("test-" + name)
			defer lis.Close()

			mux := http.NewServeMux()
			mux.Handle("GET "+DefaultPath, lis)

			var srv *httptest.Server
			if useTLS {
				srv = httptest.NewTLSServer(mux)
			} else {
				srv = httptest.NewServer(mux)
			}
			defer srv.Close()

			errCh := make(chan error, 1)
			go func() {
				conn, err := lis.Accept()
				if err != nil {
					errCh <- err
					return
				}
				defer conn.Close()
				_, err = io.Copy(conn, conn)
				errCh <- err
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
			wsURL = strings.Replace(wsURL, "https://", "wss://", 1) + DefaultPath

			var tlsCfg *tls.Config
			if useTLS {
				tlsCfg = &tls.Config{InsecureSkipVerify: true}
			}
			clientConn, err := DialContext(ctx, wsURL, tlsCfg)
			if err != nil {
				t.Fatalf("DialContext(%s): %v", wsURL, err)
			}

			payload := bytes.Repeat([]byte{0x00, 0xff, 0x42, 0x11}, 1024)
			if _, err := clientConn.Write(payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(clientConn, got); err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("echoed binary payload mismatch")
			}
			_ = clientConn.Close()

			select {
			case err := <-errCh:
				if err != nil && err != io.EOF {
					t.Fatalf("server copy error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for server echo goroutine to exit")
			}
		})
	}
}
