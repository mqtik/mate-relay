package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mqtik/mate-relay/internal/storage"
	"github.com/mqtik/mate-relay/internal/tunnel"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	f, err := os.CreateTemp("", "relay-admin-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	db, err := storage.Open(f.Name(), "pepper", "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewHandler(db, tunnel.NewRegistry(), "tunnel.mate.iwwwan.com")
}

func TestCreateAndListCodes(t *testing.T) {
	h := newTestHandler(t)

	body := bytes.NewBufferString(`{"label":"test","ttl":"24h"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/codes", body)
	w := httptest.NewRecorder()
	h.CreateCode(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["id"] == "" || resp["code"] == "" {
		t.Fatalf("expected id and code, got %v", resp)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/admin/codes", nil)
	w2 := httptest.NewRecorder()
	h.ListCodes(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 code, got %d", len(list))
	}
}

func TestRedeemCode(t *testing.T) {
	h := newTestHandler(t)

	body := bytes.NewBufferString(`{"label":"redeem-test","ttl":"24h"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/codes", body)
	w := httptest.NewRecorder()
	h.CreateCode(w, req)

	var createResp map[string]string
	json.Unmarshal(w.Body.Bytes(), &createResp)
	code := createResp["code"]

	redeemBody, _ := json.Marshal(map[string]string{
		"code":        code,
		"fingerprint": "fp-test",
		"deviceName":  "Test iPhone",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/redeem", bytes.NewBuffer(redeemBody))
	w2 := httptest.NewRecorder()
	h.Redeem(w2, req2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w2.Code, w2.Body.String())
	}

	var redeemResp map[string]any
	json.Unmarshal(w2.Body.Bytes(), &redeemResp)
	if redeemResp["deviceToken"] == nil || redeemResp["macId"] == nil || redeemResp["tunnelUrl"] == nil || redeemResp["relayHost"] == nil {
		t.Fatalf("expected deviceToken, macId, tunnelUrl, relayHost, got %v", redeemResp)
	}
}

func TestRedeemInvalidCode(t *testing.T) {
	h := newTestHandler(t)

	redeemBody, _ := json.Marshal(map[string]string{
		"code":        "BADCODE1",
		"fingerprint": "fp-test",
	})
	req := httptest.NewRequest(http.MethodPost, "/redeem", bytes.NewBuffer(redeemBody))
	w := httptest.NewRecorder()
	h.Redeem(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}
