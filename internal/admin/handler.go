package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mqtik/mate-relay/internal/storage"
	"github.com/mqtik/mate-relay/internal/tunnel"
)

type Handler struct {
	DB          *storage.DB
	Registry    *tunnel.Registry
	ControlHost string
}

func NewHandler(db *storage.DB, reg *tunnel.Registry, controlHost string) *Handler {
	return &Handler{DB: db, Registry: reg, ControlHost: controlHost}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (h *Handler) CreateCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Label string `json:"label"`
		TTL   string `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ttl := 24 * time.Hour
	if req.TTL != "" {
		if d, err := time.ParseDuration(req.TTL); err == nil {
			ttl = d
		}
	}
	id, plain, err := h.DB.CreateCode(req.Label, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":   id,
		"code": plain,
	})
}

func (h *Handler) ListCodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	codes, err := h.DB.ListCodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type codeResp struct {
		ID        string  `json:"id"`
		Label     string  `json:"label"`
		ExpiresAt int64   `json:"expiresAt"`
		CreatedAt int64   `json:"createdAt"`
		Used      bool    `json:"used"`
		Revoked   bool    `json:"revoked"`
	}
	resp := make([]codeResp, 0, len(codes))
	for _, c := range codes {
		resp = append(resp, codeResp{
			ID:        c.ID,
			Label:     c.Label,
			ExpiresAt: c.ExpiresAt.Unix(),
			CreatedAt: c.CreatedAt.Unix(),
			Used:      c.UsedAt != nil,
			Revoked:   c.RevokedAt != nil,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) RevokeCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := h.DB.RevokeCode(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := h.DB.RevokeDevice(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type redeemRequest struct {
	Code        string `json:"code"`
	Fingerprint string `json:"fingerprint"`
	DeviceName  string `json:"deviceName"`
}

func (h *Handler) Redeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req redeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Code == "" || req.Fingerprint == "" {
		writeError(w, http.StatusBadRequest, "code and fingerprint required")
		return
	}
	dev, token, err := h.DB.RedeemCode(req.Code, req.Fingerprint, req.DeviceName)
	if err == storage.ErrCodeInvalid {
		writeError(w, http.StatusUnprocessableEntity, "code invalid or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"deviceId":    dev.ID,
		"macId":       dev.MacID,
		"deviceToken": token,
		"tunnelUrl":   "wss://" + h.ControlHost + "/tunnel/control",
		"relayHost":   dev.MacID + "." + h.ControlHost,
	})
}
