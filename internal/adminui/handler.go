package adminui

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mqtik/mate-relay/internal/storage"
	"github.com/mqtik/mate-relay/internal/tunnel"
)

//go:embed page.html
var pageHTML []byte

type Handler struct {
	DB       *storage.DB
	Registry *tunnel.Registry
	password string
}

func NewHandler(db *storage.DB, reg *tunnel.Registry, password string) *Handler {
	return &Handler{DB: db, Registry: reg, password: password}
}

func (h *Handler) basicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || pass != h.password {
			w.Header().Set("WWW-Authenticate", `Basic realm="mate-relay admin"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/add", h.basicAuth(h.serveUI))
	mux.HandleFunc("/add/codes", h.basicAuth(h.handleCodes))
	mux.HandleFunc("/add/codes/", h.basicAuth(h.handleCode))
	mux.HandleFunc("/add/devices", h.basicAuth(h.handleDevices))
	mux.HandleFunc("/add/devices/", h.basicAuth(h.handleDevice))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) handleCodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		codes, err := h.DB.ListCodes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type row struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			ExpiresAt int64  `json:"expiresAt"`
			Used      bool   `json:"used"`
			Revoked   bool   `json:"revoked"`
		}
		out := make([]row, 0, len(codes))
		for _, c := range codes {
			out = append(out, row{
				ID:        c.ID,
				Label:     c.Label,
				ExpiresAt: c.ExpiresAt.Unix(),
				Used:      c.UsedAt != nil,
				Revoked:   c.RevokedAt != nil,
			})
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req struct {
			Label string `json:"label"`
			TTL   string `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		ttl := 24 * time.Hour
		if d, err := time.ParseDuration(req.TTL); err == nil && d > 0 {
			ttl = d
		}
		id, plain, err := h.DB.CreateCode(req.Label, ttl)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": id, "code": plain})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Path[len("/add/codes/"):]
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := h.DB.RevokeCode(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	devs, err := h.DB.ListDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		MacID      string `json:"macId"`
		Online     bool   `json:"online"`
		LastSeenAt int64  `json:"lastSeenAt"`
		CreatedAt  int64  `json:"createdAt"`
		Revoked    bool   `json:"revoked"`
	}
	out := make([]row, 0, len(devs))
	for _, d := range devs {
		out = append(out, row{
			ID:         d.ID,
			Name:       d.Name,
			MacID:      d.MacID,
			Online:     h.Registry.IsOnline(d.MacID),
			LastSeenAt: d.LastSeenAt.Unix(),
			CreatedAt:  d.CreatedAt.Unix(),
			Revoked:    d.RevokedAt != nil,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Path[len("/add/devices/"):]
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := h.DB.RevokeDevice(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) serveUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(pageHTML)
}
