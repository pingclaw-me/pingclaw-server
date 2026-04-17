// openclaw-mock is a tiny HTTP server that pretends to be an OpenClaw home
// agent. It accepts location-update webhook callbacks from PingClaw and
// prints them to stdout so you can verify the proactive notification flow
// end-to-end.
//
// Usage:
//
//	go run ./cmd/openclaw-mock                        # listen on :9999
//	go run ./cmd/openclaw-mock --port 9000            # listen on :9000
//	go run ./cmd/openclaw-mock --register             # also register webhook on PingClaw
//
// The OpenClaw operator owns the webhook secret end-to-end: pick or
// generate any string and pass it via --secret or the
// OPENCLAW_WEBHOOK_SECRET env var. With --register the mock sends both
// {url, secret} to PingClaw, which stores them and replays the secret
// as Authorization: Bearer on every outbound POST. The mock then
// rejects any incoming POST whose bearer doesn't match.
//
// If --secret is omitted the mock auto-generates one for you (printed at
// startup). Use a real secret in production.
//
// Authentication for the registration call itself uses the user's
// PingClaw pairing token or api key, supplied via --token or the
// PINGCLAW_TOKEN env var.
//
// To register against a remote PingClaw (e.g. ngrok), pass:
//
//	--register --pingclaw-url https://xxx.ngrok-free.dev \
//	  --webhook-url https://your-tunnel.ngrok-free.dev/location
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	port := flag.Int("port", 9999, "port to listen on")
	register := flag.Bool("register", false, "register the webhook on PingClaw on startup")
	pingclawURL := flag.String("pingclaw-url", "http://localhost:8080", "PingClaw server base URL (used with --register)")
	token := flag.String("token", os.Getenv("PINGCLAW_TOKEN"), "PingClaw pairing token or api key (used with --register, defaults to PINGCLAW_TOKEN env)")
	webhookURL := flag.String("webhook-url", "", "webhook URL to register (defaults to http://localhost:<port>/location)")
	secret := flag.String("secret", os.Getenv("OPENCLAW_WEBHOOK_SECRET"), "webhook bearer secret. The OpenClaw operator picks this; PingClaw stores it and replays it on every outbound POST. Defaults to OPENCLAW_WEBHOOK_SECRET; auto-generated if empty.")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if *secret == "" {
		*secret = "whsec_" + randomHex(16)
		slog.Info("auto-generated webhook secret (use --secret to supply your own)", "secret", *secret)
	}

	srv := &server{secret: *secret}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /location", srv.handleLocation)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "openclaw-mock ok")
	})

	addr := ":" + strconv.Itoa(*port)
	slog.Info("openclaw-mock listening", "addr", addr, "webhook_path", "/location")
	slog.Info("waiting for location events. POST to http://localhost" + addr + "/location to test directly.")

	if *register {
		hookURL := *webhookURL
		if hookURL == "" {
			hookURL = "http://localhost" + addr + "/location"
		}
		go func() {
			// Small delay so the listener is ready
			time.Sleep(200 * time.Millisecond)
			if err := registerWebhook(*pingclawURL, *token, hookURL, *secret); err != nil {
				slog.Error("webhook registration failed", "error", err)
				return
			}
			slog.Info("webhook registered with PingClaw — incoming POSTs will be verified against the configured secret")
		}()
	}

	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type server struct {
	mu     sync.Mutex
	count  int
	secret string // immutable after construction; no lock needed for reads
}

func (s *server) handleLocation(w http.ResponseWriter, r *http.Request) {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.secret)) != 1 {
		slog.Warn("rejected unauthorized POST", "remote", r.RemoteAddr, "auth_present", got != "")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	s.count++
	idx := s.count
	s.mu.Unlock()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read failed", 500)
		return
	}
	r.Body.Close()

	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "  ", "  ") != nil {
		pretty.Write(body)
	}

	fmt.Println()
	fmt.Println("─────────────────────────────────────────────")
	fmt.Printf("  LOCATION EVENT #%d  (%s)\n", idx, time.Now().Format(time.RFC3339))
	fmt.Println("─────────────────────────────────────────────")
	fmt.Println("  " + pretty.String())
	fmt.Println("─────────────────────────────────────────────")
	fmt.Println()

	var parsed struct {
		Event    string `json:"event"`
		UserID   string `json:"user_id"`
		Location struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
		Activity string `json:"activity"`
	}
	json.Unmarshal(body, &parsed)
	slog.Info("location event received",
		"event_num", idx,
		"event", parsed.Event,
		"user_id", parsed.UserID,
		"lat", parsed.Location.Lat,
		"lng", parsed.Location.Lng,
		"activity", parsed.Activity,
	)

	w.WriteHeader(200)
	fmt.Fprintln(w, "ok")
}

// registerWebhook calls PUT /pingclaw/webhook on the PingClaw server with
// both the webhook URL and the secret PingClaw should send as Bearer on
// every outbound POST.
func registerWebhook(baseURL, token, webhookURL, secret string) error {
	if token == "" {
		return fmt.Errorf("PingClaw token is empty — set PINGCLAW_TOKEN or pass --token")
	}
	if secret == "" {
		return fmt.Errorf("webhook secret is empty")
	}

	body, _ := json.Marshal(map[string]string{
		"url":    webhookURL,
		"secret": secret,
	})
	req, err := http.NewRequest("PUT", baseURL+"/pingclaw/webhook", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("webhook registered", "webhook_url", webhookURL, "response", string(respBody))
	return nil
}
