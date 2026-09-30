package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBeaconMissing(t *testing.T) {
	s := newTestServer()
	s.SetBeaconFile(filepath.Join(t.TempDir(), "missing.json"))

	req := httptest.NewRequest(http.MethodGet, "/beacon", nil)
	w := httptest.NewRecorder()
	s.beaconHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestBeaconOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.json")
	body := []byte("{\"id\":\"zhc\",\"nick\":\"captain\",\"role\":\"captain\",\"extra\":1}\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	s := newTestServer()
	s.SetBeaconFile(path)

	mux := http.NewServeMux()
	s.RegisterOnMux(mux)

	req := httptest.NewRequest(http.MethodGet, "/beacon", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", w.Code, w.Body.String())
	}
	var card beaconCard
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.ID != "zhc" || card.Nick != "captain" || card.Role != "captain" {
		t.Fatalf("card = %+v", card)
	}
	if bytesContain(w.Body.Bytes(), []byte("extra")) {
		t.Fatalf("response leaked extra field: %s", w.Body.String())
	}
}

func TestBeaconIncompleteIs404(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beacon.json")
	if err := os.WriteFile(path, []byte(`{"id":"zhc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	s.SetBeaconFile(path)

	req := httptest.NewRequest(http.MethodGet, "/beacon", nil)
	w := httptest.NewRecorder()
	s.beaconHandler(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func bytesContain(b, sub []byte) bool {
	return len(sub) == 0 || (len(b) >= len(sub) && (string(b) == string(sub) || len(b) > 0 && contains(string(b), string(sub))))
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
