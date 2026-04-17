// Package sms wraps the Twilio Messages API. The client is intentionally
// tiny — Twilio's REST API is simple enough that pulling in their full
// Go SDK isn't worth the dependency footprint or the build complexity.
package sms

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const twilioAPIBase = "https://api.twilio.com/2010-04-01"

// Client sends SMS via Twilio's Messages API.
type Client struct {
	accountSID string
	authToken  string
	fromNumber string
	httpClient *http.Client
}

// New constructs a Twilio client.
//
//   accountSID  e.g. "ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
//   authToken   the matching auth token from the Twilio console
//   fromNumber  a Twilio phone number you own (E.164, e.g. "+12025550199")
func New(accountSID, authToken, fromNumber string) *Client {
	return &Client{
		accountSID: accountSID,
		authToken:  authToken,
		fromNumber: fromNumber,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// Send delivers `body` to `to` via Twilio. `to` must be E.164 format
// (e.g. "+12125551234"). Returns nil on success; on Twilio rejection,
// returns an error containing the HTTP status and response body so the
// caller can log enough context to debug.
func (c *Client) Send(ctx context.Context, to, body string) error {
	form := url.Values{}
	form.Set("To", to)
	form.Set("From", c.fromNumber)
	form.Set("Body", body)

	endpoint := fmt.Sprintf("%s/Accounts/%s/Messages.json", twilioAPIBase, c.accountSID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.accountSID, c.authToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("twilio request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("twilio HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}
