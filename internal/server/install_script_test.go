package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dn-11/11dog/internal/server"
	"github.com/dn-11/11dog/internal/store"
)

func TestServeStaticScripts(t *testing.T) {
	installHandler := server.ServeStaticScript("install.sh")
	req1 := httptest.NewRequest(http.MethodGet, "/scripts/install.sh", nil)
	rec1 := httptest.NewRecorder()
	installHandler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("install.sh status = %d, want 200", rec1.Code)
	}
	if ct := rec1.Header().Get("Content-Type"); !strings.Contains(ct, "text/x-shellscript") {
		t.Fatalf("install.sh Content-Type = %q, want text/x-shellscript", ct)
	}

	// Query params must not alter the static script output.
	req2 := httptest.NewRequest(http.MethodGet, "/scripts/install.sh?token=ignored&nats=ws://ignored", nil)
	rec2 := httptest.NewRecorder()
	installHandler.ServeHTTP(rec2, req2)
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatal("install.sh output changed with query parameters; expected static file")
	}

	body := rec1.Body.String()
	for _, want := range []string{"--server", "--token", "--nats", "--max-concurrency", "--download-base", "--uninstall", `ExecStart=${INSTALL_BIN} --config ${CONFIG_FILE}`, `ExecReload=/bin/kill -HUP \$MAINPID`} {
		if !strings.Contains(body, want) {
			t.Fatalf("static install.sh missing %q", want)
		}
	}
	if strings.Contains(body, "{{") {
		t.Fatal("static install.sh unexpectedly contains template markers")
	}

	uninstallHandler := server.ServeStaticScript("uninstall.sh")
	ureq := httptest.NewRequest(http.MethodGet, "/scripts/uninstall.sh", nil)
	urec := httptest.NewRecorder()
	uninstallHandler.ServeHTTP(urec, ureq)
	if urec.Code != http.StatusOK || !strings.Contains(urec.Body.String(), "systemctl disable --now 11dog-agent") {
		t.Fatalf("unexpected uninstall.sh status=%d body=%s", urec.Code, urec.Body.String())
	}
}

func TestServeNodeCreds(t *testing.T) {
	st, err := store.NewSQLStore("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	tok := store.GenerateNodeToken()
	wantCreds := "-----BEGIN NATS USER JWT-----\ntest-jwt\n------END NATS USER JWT------"
	_, err = st.CreateNode(context.Background(), &store.NodeRecord{
		ID:             store.GenerateNodeID(),
		Name:           "creds-test-node",
		Region:         "HK",
		Group:          "DN11",
		ASN:            4211110101,
		MaxConcurrency: 12,
		Token:          tok,
		Creds:          wantCreds,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := server.ServeNodeCreds(st)

	// Bearer token header auth.
	req := httptest.NewRequest(http.MethodGet, "/scripts/creds", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != wantCreds {
		t.Fatalf("Bearer creds status=%d body=%q", rec.Code, rec.Body.String())
	}

	// Query token fallback.
	reqQ := httptest.NewRequest(http.MethodGet, "/scripts/creds?token="+tok, nil)
	recQ := httptest.NewRecorder()
	handler.ServeHTTP(recQ, reqQ)
	if recQ.Code != http.StatusOK || strings.TrimSpace(recQ.Body.String()) != wantCreds {
		t.Fatalf("query creds status=%d body=%q", recQ.Code, recQ.Body.String())
	}

	// Invalid & unknown tokens.
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/scripts/creds", http.StatusBadRequest},
		{"/scripts/creds?token=bad;token", http.StatusBadRequest},
		{"/scripts/creds?token=dog_00000000000000000000000000000000", http.StatusNotFound},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.target, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("GET %s status=%d, want %d", tc.target, w.Code, tc.want)
		}
	}
}
