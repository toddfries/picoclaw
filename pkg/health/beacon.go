package health

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

type beaconCard struct {
	ID   string `json:"id"`
	Nick string `json:"nick"`
	Role string `json:"role"`
}

// beaconHandler serves GET /beacon from beacon.json.
// The file is read on every request, so a rewrite is visible without reload.
// Missing or incomplete file is 404. The bus must not invent a nick.
func (s *Server) beaconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed, use GET"})
		return
	}

	f, err := os.Open(s.beaconPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "beacon not found"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "beacon unreadable"})
		return
	}
	defer f.Close()

	body, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "beacon unreadable"})
		return
	}

	var card beaconCard
	if err := json.Unmarshal(body, &card); err != nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "beacon not found"})
		return
	}
	card.ID = strings.TrimSpace(card.ID)
	card.Nick = strings.TrimSpace(card.Nick)
	card.Role = strings.TrimSpace(card.Role)
	if card.ID == "" || card.Nick == "" || card.Role == "" {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "beacon not found"})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(card)
}
