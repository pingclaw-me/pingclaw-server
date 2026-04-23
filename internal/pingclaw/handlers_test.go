package pingclaw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pingclaw-me/pingclaw-server/internal/kvstore"
)

// --- Pure function tests ---

func TestLocationKey(t *testing.T) {
	if got := locationKey("usr_abc"); got != "loc:usr_abc" {
		t.Fatalf("expected loc:usr_abc, got %s", got)
	}
}

func TestWebhookCacheKey(t *testing.T) {
	if got := webhookCacheKey("usr_abc"); got != "wh:usr_abc" {
		t.Fatalf("expected wh:usr_abc, got %s", got)
	}
}

func TestOpenclawDestCacheKey(t *testing.T) {
	if got := openclawDestCacheKey("usr_abc"); got != "oc:usr_abc" {
		t.Fatalf("expected oc:usr_abc, got %s", got)
	}
}

func TestAuthCacheKey(t *testing.T) {
	if got := authCacheKey("deadbeef"); got != "auth:deadbeef" {
		t.Fatalf("expected auth:deadbeef, got %s", got)
	}
}

func TestWebCodeKey(t *testing.T) {
	if got := webCodeKey("ABC123"); got != "webcode:ABC123" {
		t.Fatalf("expected webcode:ABC123, got %s", got)
	}
}

func TestHashToken(t *testing.T) {
	h1 := hashToken("pt_abc123")
	h2 := hashToken("pt_abc123")
	h3 := hashToken("pt_different")

	if h1 != h2 {
		t.Fatal("same input should produce same hash")
	}
	if h1 == h3 {
		t.Fatal("different inputs should produce different hashes")
	}
	if len(h1) != 64 {
		t.Fatalf("SHA-256 hex should be 64 chars, got %d", len(h1))
	}
}

func TestGenerateToken(t *testing.T) {
	tok := GenerateToken("pt_")
	if !strings.HasPrefix(tok, "pt_") {
		t.Fatalf("expected pt_ prefix, got %s", tok)
	}
	// 16 random bytes = 32 hex chars + 3 char prefix
	if len(tok) != 3+32 {
		t.Fatalf("expected 35 chars, got %d (%s)", len(tok), tok)
	}

	// Two tokens should be different
	tok2 := GenerateToken("pt_")
	if tok == tok2 {
		t.Fatal("two generated tokens should not be equal")
	}
}

func TestGenerateTokenPrefixes(t *testing.T) {
	for _, prefix := range []string{"pt_", "ak_", "ws_", "dest_"} {
		tok := GenerateToken(prefix)
		if !strings.HasPrefix(tok, prefix) {
			t.Errorf("expected prefix %s, got %s", prefix, tok)
		}
	}
}

func TestGenerateWebCode(t *testing.T) {
	code := generateWebCode()
	if len(code) != 8 {
		t.Fatalf("expected 8 chars, got %d (%s)", len(code), code)
	}

	// Should only contain allowed characters (no 0, O, I, 1)
	for _, c := range code {
		if strings.ContainsRune("01OI", c) {
			t.Fatalf("code contains ambiguous character %c: %s", c, code)
		}
	}

	// Should be uppercase
	if code != strings.ToUpper(code) {
		t.Fatalf("code should be uppercase: %s", code)
	}

	// Two codes should be different
	code2 := generateWebCode()
	if code == code2 {
		t.Fatal("two generated codes should not be equal")
	}
}

func TestFormatLocationText(t *testing.T) {
	tests := []struct {
		name     string
		lat, lon float64
		acc      float64
		source   string
		want     string
	}{
		{
			name: "northern eastern",
			lat: 51.5074, lon: 0.1278, acc: 32, source: "wifi",
			want: "Location update: 51.5074, 0.1278 ±32m (wifi)",
		},
		{
			name: "southern western",
			lat: -27.1396, lon: -109.427, acc: 8, source: "gps",
			want: "Location update: -27.1396, -109.4270 ±8m (gps)",
		},
		{
			name: "zero coordinates",
			lat: 0, lon: 0, acc: 100, source: "cell",
			want: "Location update: 0.0000, 0.0000 ±100m (cell)",
		},
		{
			name: "empty source defaults to gps",
			lat: 40.7128, lon: -74.006, acc: 15, source: "",
			want: "Location update: 40.7128, -74.0060 ±15m (gps)",
		},
		{
			name: "accuracy rounding",
			lat: 48.2085, lon: 16.3721, acc: 12.7, source: "gps",
			want: "Location update: 48.2085, 16.3721 ±13m (gps)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLocationText(tt.lat, tt.lon, tt.acc, tt.source)
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestHookPathRe(t *testing.T) {
	valid := []string{"pingclaw", "my-hook", "test123", "a", "abc-def-ghi"}
	invalid := []string{"", "has/slash", "has space", "-leadinghyphen", ".dot", "special!"}

	for _, s := range valid {
		if !hookPathRe.MatchString(s) {
			t.Errorf("expected %q to be valid hook path", s)
		}
	}
	for _, s := range invalid {
		if hookPathRe.MatchString(s) {
			t.Errorf("expected %q to be invalid hook path", s)
		}
	}
}

func TestIsPrivateHost(t *testing.T) {
	privateHosts := []string{
		"localhost",
		"LOCALHOST",
		"myhost.local",
		"printer.internal",
		"127.0.0.1",
	}
	for _, h := range privateHosts {
		if !isPrivateHost(h) {
			t.Errorf("expected %q to be private", h)
		}
	}

	// DNS failure should fail closed (return true)
	if !isPrivateHost("this-domain-definitely-does-not-exist-xyz123.invalid") {
		t.Error("DNS failure should fail closed (return private=true)")
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{
		{"XFF single", "1.2.3.4", "10.0.0.1:12345", "1.2.3.4"},
		{"XFF chain", "1.2.3.4, 10.0.0.1, 10.0.0.2", "10.0.0.3:12345", "10.0.0.2"},
		{"XFF spaces", "  1.2.3.4  ", "10.0.0.1:12345", "1.2.3.4"},
		{"no XFF with port", "", "5.6.7.8:54321", "5.6.7.8"},
		{"no XFF no port", "", "5.6.7.8", "5.6.7.8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(r); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// --- writeJSON / writeError ---

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 201, map[string]string{"status": "created"})

	if w.Code != 201 {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "created" {
		t.Fatalf("expected created, got %v", body)
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, 400, "bad request")

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "bad request" {
		t.Fatalf("expected 'bad request', got %v", body)
	}
}

// --- KVStore-backed handler tests ---

func TestReadWriteLocation(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	// Read non-existent
	loc, err := h.readLocation(ctx, "usr_test")
	if err != nil {
		t.Fatalf("readLocation error: %v", err)
	}
	if loc != nil {
		t.Fatal("expected nil location for new user")
	}

	// Write
	acc := 12.5
	err = h.writeLocation(ctx, "usr_test", cachedLocation{
		Lat:            48.2085,
		Lng:            16.3721,
		AccuracyMetres: &acc,
		Timestamp:      "2026-04-21T12:00:00Z",
		ReceivedAt:     "2026-04-21T12:00:01Z",
	})
	if err != nil {
		t.Fatalf("writeLocation error: %v", err)
	}

	// Read back
	loc, err = h.readLocation(ctx, "usr_test")
	if err != nil {
		t.Fatalf("readLocation error: %v", err)
	}
	if loc == nil {
		t.Fatal("expected location after write")
	}
	if loc.Lat != 48.2085 || loc.Lng != 16.3721 {
		t.Fatalf("wrong coords: %+v", loc)
	}
	if loc.AccuracyMetres == nil || *loc.AccuracyMetres != 12.5 {
		t.Fatalf("wrong accuracy: %v", loc.AccuracyMetres)
	}
}

func TestReadWriteLocationIsolation(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	h.writeLocation(ctx, "usr_a", cachedLocation{Lat: 1, Lng: 2, Timestamp: "t", ReceivedAt: "t"})

	loc, _ := h.readLocation(ctx, "usr_b")
	if loc != nil {
		t.Fatal("user B should have no location")
	}

	loc, _ = h.readLocation(ctx, "usr_a")
	if loc == nil || loc.Lat != 1 {
		t.Fatal("user A should have location")
	}
}

func TestLookupWebhookFromCache(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	// Pre-populate cache with a webhook
	kv.Set(ctx, webhookCacheKey("usr_test"), "http://example.com\nmysecret", 0)

	url, secret, ok := h.lookupWebhook(ctx, "usr_test")
	if !ok {
		t.Fatal("expected webhook from cache")
	}
	if url != "http://example.com" || secret != "mysecret" {
		t.Fatalf("wrong webhook: url=%q secret=%q", url, secret)
	}
}

func TestLookupWebhookCacheNoneSentinel(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	// Pre-populate with __none__ sentinel
	kv.Set(ctx, webhookCacheKey("usr_test"), webhookCacheNone, 0)

	_, _, ok := h.lookupWebhook(ctx, "usr_test")
	if ok {
		t.Fatal("expected no webhook when sentinel is cached")
	}
}

func TestLookupOpenClawDestFromCache(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	// Pre-populate cache with a destination
	destJSON := `{"destination_id":"dest_123","gateway_url":"http://gw.example.com","hook_token":"tok","hook_path":"pingclaw","action":"wake","session_key":""}`
	kv.Set(ctx, openclawDestCacheKey("usr_test"), destJSON, 0)

	dest, err := h.lookupOpenClawDest(ctx, "usr_test")
	if err != nil || dest == nil {
		t.Fatalf("expected dest from cache, err=%v", err)
	}
	if dest.GatewayURL != "http://gw.example.com" {
		t.Fatalf("wrong gateway URL: %s", dest.GatewayURL)
	}
	if dest.Action != "wake" {
		t.Fatalf("wrong action: %s", dest.Action)
	}
}

func TestLookupOpenClawDestCacheNoneSentinel(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	kv.Set(ctx, openclawDestCacheKey("usr_test"), openclawDestCacheNone, 0)

	dest, err := h.lookupOpenClawDest(ctx, "usr_test")
	if err != nil || dest != nil {
		t.Fatalf("expected nil dest with sentinel, got %v, err=%v", dest, err)
	}
}

func TestInvalidateWebhookCache(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	// Populate cache
	kv.Set(ctx, webhookCacheKey("usr_test"), "http://example.com\nsecret", 0)

	h.invalidateWebhookCache(ctx, "usr_test")

	_, err := kv.Get(ctx, webhookCacheKey("usr_test"))
	if err == nil {
		t.Fatal("cache should be invalidated")
	}
}

func TestInvalidateOpenClawDestCache(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	kv.Set(ctx, openclawDestCacheKey("usr_test"), "{}", 0)

	h.invalidateOpenClawDestCache(ctx, "usr_test")

	_, err := kv.Get(ctx, openclawDestCacheKey("usr_test"))
	if err == nil {
		t.Fatal("cache should be invalidated")
	}
}

// --- HTTP handler tests ---

func TestSocialAuthBlockedWhenNoVerifier(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv, verifier: nil}

	body := `{"provider":"apple","id_token":"fake"}`
	r := httptest.NewRequest("POST", "/pingclaw/auth/social", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.SocialAuth(w, r)

	if w.Code != 404 {
		t.Fatalf("expected 404 when verifier is nil, got %d", w.Code)
	}
}

func TestGetConfig(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv, cfg: RateLimitConfig{ChatGPTURL: "https://example.com/gpt"}}

	r := httptest.NewRequest("GET", "/pingclaw/config", nil)
	w := httptest.NewRecorder()
	h.GetConfig(w, r)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	integrations := resp["integrations"].(map[string]any)
	chatgpt := integrations["chatgpt"].(map[string]any)
	if chatgpt["url"] != "https://example.com/gpt" {
		t.Fatalf("wrong ChatGPT URL: %v", chatgpt["url"])
	}
}

func TestRequireAuthMissingToken(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}

	called := false
	handler := h.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	r := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler(w, r)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if called {
		t.Fatal("handler should not have been called")
	}
}

// --- Integration activity tracking ---

func TestRecordAndGetIntegrationActivity(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	// No activity yet
	activity := h.getIntegrationActivity(ctx, "usr_test")
	if len(activity) != 0 {
		t.Fatalf("expected empty activity, got %v", activity)
	}

	// Record MCP activity
	h.recordIntegrationActivity("usr_test", "mcp")

	activity = h.getIntegrationActivity(ctx, "usr_test")
	if activity["mcp"] == "" {
		t.Fatal("expected mcp activity timestamp")
	}
	if activity["webhook"] != "" {
		t.Fatal("webhook should have no activity")
	}
}

func TestRecordMultipleIntegrationTypes(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	h.recordIntegrationActivity("usr_test", "mcp")
	h.recordIntegrationActivity("usr_test", "webhook")
	h.recordIntegrationActivity("usr_test", "openclaw")

	activity := h.getIntegrationActivity(ctx, "usr_test")
	if len(activity) != 3 {
		t.Fatalf("expected 3 activity entries, got %d: %v", len(activity), activity)
	}
	for _, kind := range []string{"mcp", "webhook", "openclaw"} {
		if activity[kind] == "" {
			t.Fatalf("expected %s activity", kind)
		}
	}
}

func TestIntegrationActivityIsolatedPerUser(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	h.recordIntegrationActivity("usr_a", "mcp")
	h.recordIntegrationActivity("usr_b", "webhook")

	actA := h.getIntegrationActivity(ctx, "usr_a")
	actB := h.getIntegrationActivity(ctx, "usr_b")

	if actA["mcp"] == "" {
		t.Fatal("user A should have mcp activity")
	}
	if actA["webhook"] != "" {
		t.Fatal("user A should not have webhook activity")
	}
	if actB["webhook"] == "" {
		t.Fatal("user B should have webhook activity")
	}
	if actB["mcp"] != "" {
		t.Fatal("user B should not have mcp activity")
	}
}

func TestIntegrationActivityTimestampFormat(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}
	ctx := context.Background()

	h.recordIntegrationActivity("usr_test", "api")

	activity := h.getIntegrationActivity(ctx, "usr_test")
	ts := activity["api"]
	if ts == "" {
		t.Fatal("expected api timestamp")
	}
	// Should be valid RFC3339
	if !strings.Contains(ts, "T") || !strings.Contains(ts, "Z") {
		t.Fatalf("expected RFC3339 format, got %s", ts)
	}
}

func TestIntegrationKey(t *testing.T) {
	if got := integrationKey("usr_abc", "mcp"); got != "intg:usr_abc:mcp" {
		t.Fatalf("expected intg:usr_abc:mcp, got %s", got)
	}
}

func TestGetIntegrationStatusEndpoint(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}

	// Record some activity
	h.recordIntegrationActivity("usr_test", "mcp")
	h.recordIntegrationActivity("usr_test", "webhook")

	// Call the endpoint with user context
	r := httptest.NewRequest("GET", "/pingclaw/integrations/status", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxUserID, "usr_test"))
	w := httptest.NewRecorder()
	h.GetIntegrationStatus(w, r)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	activity, ok := resp["activity"].(map[string]any)
	if !ok {
		t.Fatalf("expected activity map, got %v", resp)
	}
	if activity["mcp"] == nil {
		t.Fatal("expected mcp in activity")
	}
	if activity["webhook"] == nil {
		t.Fatal("expected webhook in activity")
	}
	if activity["openclaw"] != nil {
		t.Fatal("openclaw should not be in activity (never recorded)")
	}
}

func TestRequireAuthBadPrefix(t *testing.T) {
	kv := kvstore.NewMemStore()
	defer kv.Close()
	h := &Handler{kv: kv}

	handler := h.requireAuth(func(w http.ResponseWriter, r *http.Request) {})

	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Authorization", "Basic abc123")
	w := httptest.NewRecorder()
	handler(w, r)

	if w.Code != 401 {
		t.Fatalf("expected 401 for non-Bearer, got %d", w.Code)
	}
}
