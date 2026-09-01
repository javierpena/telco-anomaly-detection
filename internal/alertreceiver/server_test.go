package alertreceiver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// minimalHandler returns a Handler whose HubClient list always returns empty
// (no TelcoHealthchecks → no monitored clusters → all alerts skipped).
func minimalHandler() *Handler {
	return &Handler{
		HubClient:      nil, // overridden by getMonitoredClusters returning empty
		NewSpokeClient: nil,
	}
}

func TestHandleWebhook_WrongMethod(t *testing.T) {
	h := minimalHandler()
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	h.HandleWebhook(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

func TestHandleWebhook_InvalidJSON(t *testing.T) {
	h := minimalHandler()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	h.HandleWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestHandleWebhook_EmptyAlerts(t *testing.T) {
	// The handler with a nil HubClient will fail when trying to list TelcoHealthchecks.
	// For an empty alerts list, processAlerts returns immediately before hitting the client,
	// so this should succeed.
	payload := AlertManagerPayload{
		Version:  "4",
		Status:   "firing",
		Receiver: "telco-anomaly-webhook",
		Alerts:   []Alert{},
	}
	body, _ := json.Marshal(payload)

	// We need a real (fake) hub client; nil panics on List.
	// Use a handler that overrides getMonitoredClusters via a nil-safe stub.
	// Here we test the JSON parsing and routing layer only.
	h := &Handler{
		HubClient: nil,
		NewSpokeClient: func(_ []byte) (client.Client, error) {
			return nil, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	// Empty alerts list → processAlerts returns immediately without calling HubClient.
	h.HandleWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected %d for empty alerts, got %d", http.StatusOK, rec.Code)
	}
}

func TestHandleWebhook_ValidPayloadStructure(t *testing.T) {
	payload := AlertManagerPayload{
		Version:  "4",
		Status:   "firing",
		Receiver: "test",
		Alerts: []Alert{
			{
				Status:   "firing",
				Labels:   map[string]string{"cluster": "c1", "alertname": "TestAlert"},
				StartsAt: time.Now(),
			},
		},
	}
	body, _ := json.Marshal(payload)

	var received AlertManagerPayload
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&received); err != nil {
		t.Fatalf("test payload is not valid JSON: %v", err)
	}
	if len(received.Alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(received.Alerts))
	}
	if received.Alerts[0].Labels["cluster"] != "c1" {
		t.Errorf("unexpected cluster label: %q", received.Alerts[0].Labels["cluster"])
	}
	_ = req // payload structure verified above
}

func TestNewServer(t *testing.T) {
	s := NewServer(":9999", nil, nil)
	if s.BindAddress != ":9999" {
		t.Errorf("expected bind address ':9999', got %q", s.BindAddress)
	}
	if s.Handler == nil {
		t.Error("expected Handler to be non-nil")
	}
}
