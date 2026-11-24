package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	errOffline      = errors.New("stream is offline")
	errClipsBlocked = errors.New("clipping is not allowed for this channel")
	errUpstream     = errors.New("twitch request failed")
)

type twitchClient struct {
	http         *http.Client
	authURL      string
	apiURL       string
	clientID     string
	clientSecret string
	refreshToken string
	broadcaster  string

	mu          sync.Mutex
	accessToken string
	expiry      time.Time
	now         func() time.Time
	poll        time.Duration
}

func newTwitchClient(clientID, secret, refresh, broadcaster string) *twitchClient {
	return &twitchClient{
		http:         &http.Client{Timeout: 10 * time.Second},
		authURL:      "https://id.twitch.tv",
		apiURL:       "https://api.twitch.tv/helix",
		clientID:     clientID,
		clientSecret: secret,
		refreshToken: refresh,
		broadcaster:  broadcaster,
		now:          time.Now,
		poll:         2 * time.Second,
	}
}

// token returns a cached access token, refreshing it a minute before expiry.
// Confidential-client refresh tokens do not rotate, so nothing is persisted.
func (c *twitchClient) token(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.accessToken != "" && c.now().Add(time.Minute).Before(c.expiry) {
		return c.accessToken, nil
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.refreshToken},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: refresh: %v", errUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: refresh returned %d", errUpstream, resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.AccessToken == "" {
		return "", fmt.Errorf("%w: refresh response unreadable", errUpstream)
	}
	c.accessToken = body.AccessToken
	c.expiry = c.now().Add(time.Duration(body.ExpiresIn) * time.Second)
	return c.accessToken, nil
}

func (c *twitchClient) helix(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := c.token(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path+"?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Client-Id", c.clientID)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errUpstream, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
}

// createClip makes a clip of the live stream and waits until Twitch can serve
// it, bounded by ctx, so the returned link is not a 404.
func (c *twitchClient) createClip(ctx context.Context) (string, error) {
	resp, err := c.helix(ctx, http.MethodPost, "/clips", url.Values{"broadcaster_id": {c.broadcaster}})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
	case http.StatusNotFound:
		return "", errOffline
	case http.StatusForbidden:
		return "", errClipsBlocked
	default:
		return "", fmt.Errorf("%w: create returned %d", errUpstream, resp.StatusCode)
	}
	var created struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&created); err != nil || len(created.Data) == 0 {
		return "", fmt.Errorf("%w: create response unreadable", errUpstream)
	}
	id := created.Data[0].ID
	if c.waitReady(ctx, id) {
		return "https://clips.twitch.tv/" + id, nil
	}
	return "", fmt.Errorf("%w: clip %s not ready in time", errUpstream, id)
}

func (c *twitchClient) waitReady(ctx context.Context, id string) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(c.poll):
		}
		resp, err := c.helix(ctx, http.MethodGet, "/clips", url.Values{"id": {id}})
		if err != nil {
			return false
		}
		var got struct {
			Data []struct{ ID string } `json:"data"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&got)
		resp.Body.Close()
		if err == nil && len(got.Data) > 0 {
			return true
		}
	}
}
