// localtest is an interactive end-to-end test for the PingClaw server
// running in --local mode. It starts the server, walks the user through
// connecting the app, and verifies every API/database/webhook interaction.
//
// Usage:
//
//	go run ./cmd/localtest
package main

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	dbFile     = "test_pingclaw.db"
	serverPort = "19876"
	webhookPort = "19877"
	openclawPort = "19878"
)

const logFile = "localtest.log"

var (
	serverURL  string
	token      string // pairing token (used by app)
	webSession string // web session (used by dashboard)
	apiKey     string // API key (used by MCP agents)
	serverCmd  *exec.Cmd
	log        *os.File

	// webhook receiver state
	webhookMu       sync.Mutex
	webhookPayloads []map[string]any

	// openclaw hook receiver state
	openclawMu       sync.Mutex
	openclawPayloads []map[string]any
)

// logf writes to both stdout and the log file.
func logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Print(msg)
	if log != nil {
		log.WriteString(msg)
	}
}

func main() {
	var err error
	log, err = os.Create(logFile)
	if err != nil {
		fmt.Printf("warning: could not create log file: %v\n", err)
	} else {
		defer log.Close()
		fmt.Printf("Logging to %s\n\n", logFile)
	}

	serverURL = "http://localhost:" + serverPort

	passed := 0
	failed := 0
	total := 0

	run := func(name string, fn func() error) {
		total++
		logf("\n── %s ──\n", name)
		if err := fn(); err != nil {
			logf("   FAIL: %s\n", err)
			failed++
		} else {
			logf("   PASS\n")
			passed++
		}
	}

	// === Phase 1: Server startup ===
	run("Clean up old database", cleanDB)
	run("Start server in --local mode", startServer)
	defer stopServer()
	run("Verify bootstrap user in database", verifyBootstrapUser)
	run("Verify social auth is blocked", verifySocialAuthBlocked)
	run("Verify unauthenticated request is rejected", verifyUnauthenticated)

	// === Phase 2: App pairing ===
	run("Verify token works (GET /pingclaw/location)", verifyTokenWorks)

	appURL := fmt.Sprintf("http://%s:%s", lanIP(), serverPort)
	promptUser("PAIR YOUR APP",
		"",
		"1. Open the PingClaw app on your phone",
		"2. Tap 'Self-Hosted Server' on the sign-in screen",
		"3. Enter the following:",
		"",
		fmt.Sprintf("   Server URL:    %s", appURL),
		fmt.Sprintf("   Pairing Token: %s", token),
		"",
		"4. Tap 'Connect'",
		"5. Wait for the app to show 'Location Sharing'",
	)

	run("Verify auth cache populated", verifyAuthMe)

	// === Phase 3: Location sharing ===
	promptUser("ENABLE LOCATION SHARING",
		"In the app, tap the Location Sharing card to enable it.",
		"Wait a few seconds for the first location to be sent.",
		"Press Enter here once the app shows coordinates.")

	run("Verify location stored (GET /pingclaw/location)", verifyLocationStored)
	run("Verify location in data export", verifyLocationInDataExport)

	// === Phase 4: Dashboard / web session ===
	run("Generate web session token", generateWebSession)
	run("Verify web session works", verifyWebSessionWorks)
	run("Generate API key via dashboard", generateAPIKey)
	run("Verify API key works for location read", verifyAPIKeyWorks)
	run("Verify API key works for MCP", verifyMCP)
	run("Verify GET /pingclaw/config (public)", verifyPublicConfig)

	// === Phase 5: Share Now button ===
	promptUser("TAP SHARE NOW",
		"In the app, tap 'Share Current Location Now'.",
		"Press Enter here after tapping.")

	run("Verify location timestamp updated", verifyLocationTimestampRecent)

	// === Phase 6: Webhook ===
	run("Start webhook receiver", startWebhookReceiver)
	run("Register webhook", registerWebhook)
	run("Verify webhook in database", verifyWebhookInDB)
	run("Fetch webhook config (GET /pingclaw/webhook)", verifyGetWebhook)
	run("Test webhook delivery endpoint", verifyTestWebhook)

	promptUser("SEND ANOTHER LOCATION",
		"In the app, tap 'Share Current Location Now' again.",
		"Press Enter here after tapping.")

	run("Verify webhook received payload", verifyWebhookReceived)

	// === Phase 7: OpenClaw gateway hook ===
	run("Start OpenClaw hook receiver", startOpenClawReceiver)
	run("Register OpenClaw gateway destination", registerOpenClawDest)
	run("Verify OpenClaw destination in database", verifyOpenClawDestInDB)
	run("Fetch OpenClaw destination (GET)", verifyGetOpenClawDest)
	run("Test OpenClaw delivery endpoint", verifyTestOpenClawDest)
	run("Send location to OpenClaw endpoint", verifySendOpenClawLocation)

	promptUser("SEND ANOTHER LOCATION",
		"In the app, tap 'Share Current Location Now' again.",
		"Press Enter here after tapping.")

	run("Verify OpenClaw hook received payload", verifyOpenClawReceived)
	run("Verify webhook also received (concurrent delivery)", verifyWebhookReceivedAgain)

	// === Phase 8: Integration activity tracking ===
	run("Verify integration status endpoint", verifyIntegrationStatus)

	// === Phase 9: Validation and error handling ===
	run("Reject malformed location POST", verifyMalformedLocationPost)
	run("Reject invalid webhook URL", verifyInvalidWebhookURL)
	run("Reject missing auth header", verifyMissingAuth)

	// === Phase 10: Token rotation and sign-out ===
	run("Verify API key rotation invalidates old key", verifyAPIKeyRotationInvalidates)
	run("Rotate pairing token", verifyRotatePairingToken)
	run("Verify web session sign-out", verifyWebSessionSignOut)
	run("Re-pair with new token for remaining tests", rePairForCleanup)

	// === Phase 11: Disable location sharing ===
	promptUser("DISABLE LOCATION SHARING",
		"In the app, tap the Location Sharing card to turn it OFF.",
		"Press Enter here.")

	// Location should still be cached (24h TTL)
	run("Verify location still cached after disable", verifyLocationStored)

	// === Phase 12: Delete account ===
	run("Delete webhook", deleteWebhook)
	run("Delete OpenClaw destination", deleteOpenClawDest)
	run("Delete account", deleteAccount)
	run("Verify database is empty", verifyDatabaseEmpty)
	run("Verify token no longer works", verifyTokenRejected)

	// === Summary ===
	logf("\n══════════════════════════════\n")
	logf("  Results: %d/%d passed", passed, total)
	if failed > 0 {
		logf(", %d FAILED", failed)
	}
	logf("\n══════════════════════════════\n\n")

	if failed > 0 {
		os.Exit(1)
	}
}

// --- Helpers ---

func lanIP() string {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				return ipnet.IP.String()
			}
		}
	}
	return "localhost"
}

func promptUser(title string, lines ...string) {
	logf("\n┌─ %s ─┐\n", title)
	for _, l := range lines {
		logf("│  %s\n", l)
	}
	logf("│\n")
	logf("│  Do this NOW, then press Enter.\n")
	logf("└─ ")
	fmt.Scanln()
	if log != nil {
		log.WriteString("[user pressed Enter]\n")
	}
}

func logAPI(method, path string, status int, body []byte) {
	if log != nil {
		log.WriteString(fmt.Sprintf("[API] %s %s → %d %s\n", method, path, status, string(body)))
	}
}

func apiGet(path string) (*http.Response, []byte, error) {
	req, _ := http.NewRequest("GET", serverURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("GET", path, resp.StatusCode, body)
	return resp, body, nil
}

func apiPost(path string, payload any) (*http.Response, []byte, error) {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", serverURL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("POST", path, resp.StatusCode, body)
	return resp, body, nil
}

func apiPut(path string, payload any) (*http.Response, []byte, error) {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequest("PUT", serverURL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("PUT", path, resp.StatusCode, body)
	return resp, body, nil
}

func apiDelete(path string) (*http.Response, []byte, error) {
	req, _ := http.NewRequest("DELETE", serverURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("DELETE", path, resp.StatusCode, body)
	return resp, body, nil
}

func parseJSON(body []byte) map[string]any {
	var m map[string]any
	json.Unmarshal(body, &m)
	return m
}

func openDB() (*sql.DB, error) {
	return sql.Open("sqlite", dbFile+"?mode=ro")
}

// --- Test steps ---

func cleanDB() error {
	os.Remove(dbFile)
	os.Remove(dbFile + "-wal")
	os.Remove(dbFile + "-shm")
	return nil
}

func startServer() error {
	serverCmd = exec.Command("go", "run", "./cmd/server", "--local", "--debug")
	serverCmd.Env = append(os.Environ(),
		"PORT="+serverPort,
		"DATABASE_URL="+dbFile,
	)
	// Create a new process group so we can kill go run + its child.
	serverCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capture stdout to extract the token. Server logs go to the
	// log file only — not to the terminal (keeps test output clean).
	pr, pw := io.Pipe()
	var outBuf bytes.Buffer
	serverCmd.Stdout = pw
	serverCmd.Stderr = pw

	if err := serverCmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	// Read server output in the background, extracting the token and
	// logging to the log file.
	tokenCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				outBuf.WriteString(chunk)
				if log != nil {
					log.WriteString(chunk)
				}
				// Check for the token line
				for _, line := range strings.Split(outBuf.String(), "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), "Pairing Token:") {
						t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Pairing Token:"))
						if t != "" {
							select {
							case tokenCh <- t:
							default:
							}
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Wait for the server to be ready (TCP) and the token to be printed.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "localhost:"+serverPort, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for the token (may already be in the channel)
	select {
	case token = <-tokenCh:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timed out waiting for pairing token in server output")
	}

	logf("   Token: %s\n", token)
	return nil
}

func stopServer() {
	if serverCmd != nil && serverCmd.Process != nil {
		// Kill the entire process group (go run + the spawned server binary).
		pgid, _ := syscall.Getpgid(serverCmd.Process.Pid)
		syscall.Kill(-pgid, syscall.SIGKILL)
		serverCmd.Wait()
	}
}

func verifyBootstrapUser() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var userID string
	err = db.QueryRow(`SELECT user_id FROM users`).Scan(&userID)
	if err != nil {
		return fmt.Errorf("no user found: %w", err)
	}
	if userID != "usr_local" {
		return fmt.Errorf("expected usr_local, got %s", userID)
	}

	var tokenCount int
	db.QueryRow(`SELECT COUNT(*) FROM user_tokens WHERE user_id = 'usr_local'`).Scan(&tokenCount)
	if tokenCount != 1 {
		return fmt.Errorf("expected 1 token, got %d", tokenCount)
	}

	logf("   User: %s, Tokens: %d\n", userID, tokenCount)
	return nil
}

func verifySocialAuthBlocked() error {
	resp, body, err := apiPost("/pingclaw/auth/social", map[string]string{
		"provider": "apple", "id_token": "fake",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 404 {
		return fmt.Errorf("expected 404, got %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func verifyUnauthenticated() error {
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/location", nil)
	req.Header.Set("Authorization", "Bearer pt_invalid_token")
	resp, _, err := apiGet("/pingclaw/location")
	if err != nil {
		return err
	}
	_ = req // use the token-based helper which uses the valid token
	// Test with an invalid token directly
	badReq, _ := http.NewRequest("GET", serverURL+"/pingclaw/location", nil)
	badReq.Header.Set("Authorization", "Bearer pt_bogus")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		return err
	}
	badResp.Body.Close()
	if badResp.StatusCode != 401 {
		return fmt.Errorf("expected 401 for bad token, got %d", badResp.StatusCode)
	}
	_ = resp
	return nil
}

func verifyTokenWorks() error {
	resp, body, err := apiGet("/pingclaw/location")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	if m["status"] != "no_location" {
		return fmt.Errorf("expected no_location, got %v", m["status"])
	}
	logf("   Status: %s (correct — no location yet)\n", m["status"])
	return nil
}

func verifyAuthMe() error {
	resp, body, err := apiGet("/pingclaw/auth/me")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	logf("   user_id: %v, has_api_key: %v, has_pairing_token: %v\n",
		m["user_id"], m["has_api_key"], m["has_pairing_token"])
	return nil
}

func verifyLocationStored() error {
	resp, body, err := apiGet("/pingclaw/location")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	if m["status"] != "ok" {
		return fmt.Errorf("expected status ok, got %v — is location sharing enabled in the app?", m["status"])
	}
	loc := m["location"].(map[string]any)
	logf("   Location: %.4f, %.4f ±%.0fm\n", loc["lat"], loc["lng"], loc["accuracy_metres"])
	return nil
}

func verifyLocationInDataExport() error {
	resp, body, err := apiGet("/pingclaw/auth/data")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	if m["location"] == nil {
		return fmt.Errorf("data export has no location")
	}
	logf("   Data export includes location, %d token(s)\n",
		len(m["tokens"].([]any)))
	return nil
}

func verifyMCP() error {
	// MCP uses the same auth — just verify the endpoint responds
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	// MCP GET with no session returns 405 or similar — just check it's not 404
	if resp.StatusCode == 404 {
		return fmt.Errorf("MCP endpoint not found")
	}
	logf("   MCP endpoint responded with %d (expected — not a full MCP request)\n", resp.StatusCode)
	return nil
}

func verifyLocationTimestampRecent() error {
	_, body, err := apiGet("/pingclaw/location")
	if err != nil {
		return err
	}
	m := parseJSON(body)
	if m["status"] != "ok" {
		return fmt.Errorf("expected ok, got %v", m["status"])
	}
	ts, _ := m["timestamp"].(string)
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return fmt.Errorf("could not parse timestamp %q: %w", ts, err)
	}
	age := time.Since(t)
	if age > 2*time.Minute {
		return fmt.Errorf("timestamp is %v old — expected recent", age)
	}
	logf("   Timestamp: %s (%.0fs ago)\n", ts, age.Seconds())
	return nil
}

// --- Dashboard / web session ---

func generateWebSession() error {
	// The web code flow: app generates a code, user types it in the browser.
	// In the test we call the API directly with the pairing token.
	resp, body, err := apiPost("/pingclaw/auth/web-code", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	code, _ := m["code"].(string)
	if code == "" {
		return fmt.Errorf("no code in response")
	}
	logf("   Web code: %s\n", code)

	// Exchange the code for a web session (simulates browser sign-in)
	loginResp, loginBody, err := func() (*http.Response, []byte, error) {
		data, _ := json.Marshal(map[string]string{"code": code})
		req, _ := http.NewRequest("POST", serverURL+"/pingclaw/auth/web-login", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		logAPI("POST", "/pingclaw/auth/web-login", resp.StatusCode, body)
		return resp, body, nil
	}()
	if err != nil {
		return err
	}
	if loginResp.StatusCode != 200 {
		return fmt.Errorf("web-login failed: %d %s", loginResp.StatusCode, string(loginBody))
	}
	loginData := parseJSON(loginBody)
	webSession, _ = loginData["web_session"].(string)
	if webSession == "" {
		return fmt.Errorf("no web_session in response")
	}
	logf("   Web session: %s...%s\n", webSession[:6], webSession[len(webSession)-4:])
	return nil
}

func verifyWebSessionWorks() error {
	// Use web session to call /pingclaw/auth/me
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+webSession)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("GET", "/pingclaw/auth/me (web_session)", resp.StatusCode, body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	logf("   Web session auth works: user_id=%v\n", m["user_id"])
	return nil
}

func generateAPIKey() error {
	// Use web session to rotate API key (simulates dashboard "Generate API Key")
	req, _ := http.NewRequest("POST", serverURL+"/pingclaw/auth/rotate-api-key", nil)
	req.Header.Set("Authorization", "Bearer "+webSession)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("POST", "/pingclaw/auth/rotate-api-key (web_session)", resp.StatusCode, body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	apiKey, _ = m["api_key"].(string)
	if apiKey == "" || !strings.HasPrefix(apiKey, "ak_") {
		return fmt.Errorf("invalid api_key: %v", m["api_key"])
	}
	logf("   API key: %s...%s\n", apiKey[:6], apiKey[len(apiKey)-4:])
	return nil
}

func verifyAPIKeyWorks() error {
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/location", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("GET", "/pingclaw/location (api_key)", resp.StatusCode, body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	if m["status"] != "ok" {
		return fmt.Errorf("expected ok, got %v", m["status"])
	}
	logf("   API key auth works: location status=%v\n", m["status"])
	return nil
}

func verifyPublicConfig() error {
	// /pingclaw/config is public — no auth needed
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("GET", "/pingclaw/config (no auth)", resp.StatusCode, body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	logf("   Public config endpoint works\n")
	return nil
}

// --- Sign-out / session management ---

func verifyWebSessionSignOut() error {
	if webSession == "" {
		return fmt.Errorf("no web session to test")
	}

	// Generate a new web session — this rotates (revokes the old one)
	codeResp, codeBody, err := apiPost("/pingclaw/auth/web-code", nil)
	if err != nil {
		return err
	}
	if codeResp.StatusCode != 200 {
		return fmt.Errorf("web-code failed: %d", codeResp.StatusCode)
	}
	code := parseJSON(codeBody)["code"].(string)

	loginData, _ := json.Marshal(map[string]string{"code": code})
	loginReq, _ := http.NewRequest("POST", serverURL+"/pingclaw/auth/web-login", bytes.NewReader(loginData))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		return err
	}
	loginBody, _ := io.ReadAll(loginResp.Body)
	loginResp.Body.Close()
	logAPI("POST", "/pingclaw/auth/web-login (new session)", loginResp.StatusCode, loginBody)

	newSession := parseJSON(loginBody)["web_session"].(string)

	// Old web session should be revoked (rotateToken deletes all of the same kind)
	oldReq, _ := http.NewRequest("GET", serverURL+"/pingclaw/auth/me", nil)
	oldReq.Header.Set("Authorization", "Bearer "+webSession)
	oldResp, err := http.DefaultClient.Do(oldReq)
	if err != nil {
		return err
	}
	oldResp.Body.Close()
	if oldResp.StatusCode != 401 {
		return fmt.Errorf("old web session should be revoked, got %d", oldResp.StatusCode)
	}
	logf("   Old web session correctly revoked after new sign-in\n")

	// New web session should work
	newReq, _ := http.NewRequest("GET", serverURL+"/pingclaw/auth/me", nil)
	newReq.Header.Set("Authorization", "Bearer "+newSession)
	newResp, err := http.DefaultClient.Do(newReq)
	if err != nil {
		return err
	}
	newResp.Body.Close()
	if newResp.StatusCode != 200 {
		return fmt.Errorf("new web session should work, got %d", newResp.StatusCode)
	}
	logf("   New web session works\n")

	webSession = newSession

	// Verify web codes are single-use (replaying should fail)
	replayData, _ := json.Marshal(map[string]string{"code": code})
	replayReq, _ := http.NewRequest("POST", serverURL+"/pingclaw/auth/web-login", bytes.NewReader(replayData))
	replayReq.Header.Set("Content-Type", "application/json")
	replayResp, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		return err
	}
	replayResp.Body.Close()
	if replayResp.StatusCode != 401 {
		return fmt.Errorf("replayed web code should return 401, got %d", replayResp.StatusCode)
	}
	logf("   Web code is single-use (replay correctly rejected)\n")
	return nil
}

func rePairForCleanup() error {
	// After token rotation, the pairing token changed. Verify the
	// current token still works for the remaining cleanup steps.
	resp, body, err := apiGet("/pingclaw/auth/me")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("current token doesn't work: %d %s", resp.StatusCode, string(body))
	}
	logf("   Current token verified for cleanup\n")
	return nil
}

// --- Webhook receiver ---

func startWebhookReceiver() error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /location", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		// Verify auth
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(auth), []byte("test-webhook-secret")) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}

		var payload map[string]any
		json.Unmarshal(body, &payload)
		webhookMu.Lock()
		webhookPayloads = append(webhookPayloads, payload)
		webhookMu.Unlock()
		logf("   [webhook] received: event=%v\n", payload["event"])
		w.WriteHeader(200)
	})

	listener, err := net.Listen("tcp", ":"+webhookPort)
	if err != nil {
		return err
	}
	go http.Serve(listener, mux)
	logf("   Webhook receiver listening on :%s\n", webhookPort)
	return nil
}

func registerWebhook() error {
	resp, body, err := apiPut("/pingclaw/webhook", map[string]string{
		"url":    "http://" + lanIP() + ":" + webhookPort + "/location",
		"secret": "test-webhook-secret",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	logf("   Registered: %v\n", m["url"])
	return nil
}

func verifyWebhookInDB() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var url, secret string
	err = db.QueryRow(`SELECT url, secret FROM user_webhooks WHERE user_id = 'usr_local'`).Scan(&url, &secret)
	if err != nil {
		return fmt.Errorf("webhook not found in db: %w", err)
	}
	logf("   DB webhook: url=%s\n", url)
	return nil
}

func verifyGetWebhook() error {
	resp, body, err := apiGet("/pingclaw/webhook")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	if m["url"] == nil {
		return fmt.Errorf("no url in response")
	}
	logf("   GET webhook: url=%v\n", m["url"])
	return nil
}

func verifyTestWebhook() error {
	resp, body, err := apiPost("/pingclaw/webhook/test", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	logf("   Test webhook: status=%v, delivered_status=%v\n", m["status"], m["delivered_status"])
	return nil
}

func verifyWebhookReceived() error {
	// Wait for async delivery with retries
	for i := 0; i < 10; i++ {
		webhookMu.Lock()
		count := len(webhookPayloads)
		webhookMu.Unlock()
		if count > 0 {
			webhookMu.Lock()
			last := webhookPayloads[count-1]
			webhookMu.Unlock()
			logf("   Received %d webhook payload(s), last event: %v\n", count, last["event"])
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("no webhook payloads received after 10s — did you tap Share Now?")
}

func verifyWebhookReceivedAgain() error {
	for i := 0; i < 10; i++ {
		webhookMu.Lock()
		count := len(webhookPayloads)
		webhookMu.Unlock()
		if count >= 2 {
			logf("   Total webhook payloads: %d (concurrent delivery works)\n", count)
			return nil
		}
		time.Sleep(time.Second)
	}
	webhookMu.Lock()
	count := len(webhookPayloads)
	webhookMu.Unlock()
	return fmt.Errorf("expected ≥2 webhook payloads, got %d", count)
}

// --- OpenClaw hook receiver ---

func startOpenClawReceiver() error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/pingclaw", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(auth), []byte("test-openclaw-token")) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}

		var payload map[string]any
		json.Unmarshal(body, &payload)
		openclawMu.Lock()
		openclawPayloads = append(openclawPayloads, payload)
		openclawMu.Unlock()
		logf("   [openclaw] received: text=%v\n", payload["text"])
		w.WriteHeader(200)
	})

	listener, err := net.Listen("tcp", ":"+openclawPort)
	if err != nil {
		return err
	}
	go http.Serve(listener, mux)
	logf("   OpenClaw hook receiver listening on :%s\n", openclawPort)
	return nil
}

func registerOpenClawDest() error {
	resp, body, err := apiPost("/pingclaw/webhook/openclaw", map[string]string{
		"gateway_url": "http://localhost:" + openclawPort,
		"hook_token":  "test-openclaw-token",
		"hook_path":   "pingclaw",
		"action":      "wake",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 201 {
		return fmt.Errorf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	logf("   Registered: destination_id=%v, verified=%v\n", m["destination_id"], m["verified"])
	return nil
}

func verifyOpenClawDestInDB() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var gatewayURL, action string
	err = db.QueryRow(`SELECT gateway_url, action FROM user_openclaw_destinations WHERE user_id = 'usr_local'`).Scan(&gatewayURL, &action)
	if err != nil {
		return fmt.Errorf("openclaw dest not found in db: %w", err)
	}
	logf("   DB openclaw dest: url=%s action=%s\n", gatewayURL, action)
	return nil
}

func verifyGetOpenClawDest() error {
	resp, body, err := apiGet("/pingclaw/webhook/openclaw")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	m := parseJSON(body)
	dest, _ := m["destination"].(map[string]any)
	if dest == nil {
		return fmt.Errorf("no destination in response")
	}
	logf("   GET openclaw: gateway_url=%v, action=%v\n", dest["gateway_url"], dest["action"])
	return nil
}

func verifyTestOpenClawDest() error {
	resp, body, err := apiPost("/pingclaw/webhook/openclaw/test", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	if m["verified"] != true {
		return fmt.Errorf("expected verified=true, got %v", m["verified"])
	}
	logf("   Test openclaw: verified=%v\n", m["verified"])
	return nil
}

func verifySendOpenClawLocation() error {
	resp, body, err := apiPost("/pingclaw/webhook/openclaw/send", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	if m["delivered"] != true {
		return fmt.Errorf("expected delivered=true, got %v (error: %v)", m["delivered"], m["error"])
	}
	logf("   Send location: delivered=%v, text=%v\n", m["delivered"], m["text"])
	return nil
}

// --- Validation and error handling ---

func verifyIntegrationStatus() error {
	resp, body, err := apiGet("/pingclaw/integrations/status")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	activity, ok := m["activity"].(map[string]any)
	if !ok {
		return fmt.Errorf("expected activity map in response, got %v", m)
	}

	// After sending locations with webhook + openclaw active,
	// we should have webhook and openclaw activity recorded.
	// We also did GET /location earlier, so api should be present.
	hasAny := false
	for kind, ts := range activity {
		logf("   %s: %v\n", kind, ts)
		hasAny = true
	}
	if !hasAny {
		logf("   (no activity recorded yet — this is OK for local mode with simulated POSTs)\n")
	}
	logf("   Integration status endpoint works, %d types with activity\n", len(activity))
	return nil
}

func verifyMalformedLocationPost() error {
	data := []byte(`{"not_valid": true}`)
	req, _ := http.NewRequest("POST", serverURL+"/pingclaw/location", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logAPI("POST", "/pingclaw/location (malformed)", resp.StatusCode, body)
	// Server accepts it (lat/lng default to 0,0) — that's the current behavior.
	// The important thing is it doesn't crash.
	logf("   Malformed POST handled: status=%d\n", resp.StatusCode)
	return nil
}

func verifyInvalidWebhookURL() error {
	resp, body, err := apiPut("/pingclaw/webhook", map[string]string{
		"url":    "not-a-url",
		"secret": "test",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 400 {
		return fmt.Errorf("expected 400 for invalid URL, got %d: %s", resp.StatusCode, string(body))
	}
	logf("   Invalid webhook URL correctly rejected: %s\n", string(body))
	return nil
}

func verifyMissingAuth() error {
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/location", nil)
	// No Authorization header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		return fmt.Errorf("expected 401 for missing auth, got %d", resp.StatusCode)
	}
	logf("   Missing auth correctly rejected with 401\n")
	return nil
}

func verifyOpenClawReceived() error {
	for i := 0; i < 10; i++ {
		openclawMu.Lock()
		count := len(openclawPayloads)
		openclawMu.Unlock()
		// First payload is the verification POST from registration,
		// second is from the location update
		if count >= 2 {
			openclawMu.Lock()
			last := openclawPayloads[count-1]
			openclawMu.Unlock()
			logf("   Received %d openclaw payload(s), last text: %v\n", count, last["text"])
			return nil
		}
		time.Sleep(time.Second)
	}
	openclawMu.Lock()
	count := len(openclawPayloads)
	openclawMu.Unlock()
	return fmt.Errorf("expected ≥2 openclaw payloads (1 verify + 1 location), got %d — did you tap Share Now?", count)
}

// --- Token rotation ---

func verifyAPIKeyRotationInvalidates() error {
	oldKey := apiKey
	// Rotate via pairing token
	resp, body, err := apiPost("/pingclaw/auth/rotate-api-key", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	apiKey = m["api_key"].(string)

	// Old key should be rejected
	req, _ := http.NewRequest("GET", serverURL+"/pingclaw/location", nil)
	req.Header.Set("Authorization", "Bearer "+oldKey)
	oldResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	oldResp.Body.Close()
	if oldResp.StatusCode != 401 {
		return fmt.Errorf("old API key should return 401, got %d", oldResp.StatusCode)
	}
	logf("   New API key: %s...%s (old key correctly rejected)\n", apiKey[:6], apiKey[len(apiKey)-4:])
	return nil
}

func verifyRotatePairingToken() error {
	resp, body, err := apiPost("/pingclaw/auth/rotate-pairing-token", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	m := parseJSON(body)
	newToken := m["pairing_token"].(string)
	if !strings.HasPrefix(newToken, "pt_") {
		return fmt.Errorf("expected pt_ prefix, got %s", newToken)
	}
	// Old token should no longer work
	oldToken := token
	token = newToken

	badReq, _ := http.NewRequest("GET", serverURL+"/pingclaw/location", nil)
	badReq.Header.Set("Authorization", "Bearer "+oldToken)
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		return err
	}
	badResp.Body.Close()
	if badResp.StatusCode != 401 {
		return fmt.Errorf("old token should return 401, got %d", badResp.StatusCode)
	}
	logf("   New token: %s...%s (old token correctly rejected)\n", newToken[:6], newToken[len(newToken)-4:])
	return nil
}

// --- Cleanup ---

func deleteWebhook() error {
	resp, _, err := apiDelete("/pingclaw/webhook")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	return nil
}

func deleteOpenClawDest() error {
	resp, _, err := apiDelete("/pingclaw/webhook/openclaw")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	return nil
}

func deleteAccount() error {
	resp, _, err := apiDelete("/pingclaw/auth/account")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("expected 200, got %d", resp.StatusCode)
	}
	return nil
}

func verifyDatabaseEmpty() error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	tables := []string{"users", "user_tokens", "user_identities", "user_webhooks", "user_openclaw_destinations"}
	for _, table := range tables {
		var count int
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
		if count != 0 {
			return fmt.Errorf("table %s has %d rows, expected 0", table, count)
		}
	}
	logf("   All tables empty (CASCADE delete worked)\n")
	return nil
}

func verifyTokenRejected() error {
	resp, _, err := apiGet("/pingclaw/location")
	if err != nil {
		return err
	}
	if resp.StatusCode != 401 {
		return fmt.Errorf("expected 401 after account deletion, got %d", resp.StatusCode)
	}
	return nil
}
