package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClips struct {
	link  string
	err   error
	calls int
}

func (f *fakeClips) createClip(context.Context) (string, error) {
	f.calls++
	return f.link, f.err
}

func newTestServer(f *fakeClips) *server {
	return &server{
		key:     "secret",
		clips:   f,
		limiter: newLimiter(10*time.Second, 30*time.Second),
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func get(s *server, target string) (int, string) {
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String()
}

func TestClipRejectsBadKey(t *testing.T) {
	f := &fakeClips{link: "https://clips.twitch.tv/x"}
	for _, target := range []string{"/clip", "/clip?key=wrong&user=a"} {
		if code, _ := get(newTestServer(f), target); code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", target, code)
		}
	}
	if f.calls != 0 {
		t.Errorf("twitch called %d times for unauthorized requests", f.calls)
	}
}

func TestClipSuccessAndCooldown(t *testing.T) {
	f := &fakeClips{link: "https://clips.twitch.tv/abc"}
	s := newTestServer(f)
	code, body := get(s, "/clip?key=secret&user=viewer1")
	if code != 200 || !strings.Contains(body, "https://clips.twitch.tv/abc") || !strings.Contains(body, "viewer1") {
		t.Fatalf("got %d %q", code, body)
	}
	_, body = get(s, "/clip?key=secret&user=viewer2")
	if !strings.Contains(body, "cooldown") {
		t.Errorf("global cooldown not applied: %q", body)
	}
	if f.calls != 1 {
		t.Errorf("twitch called %d times, want 1", f.calls)
	}
}

func TestClipErrorMessages(t *testing.T) {
	cases := map[string]error{
		"offline":  errOffline,
		"disabled": errClipsBlocked,
		"failed":   errors.New("boom"),
	}
	for want, err := range cases {
		_, body := get(newTestServer(&fakeClips{err: err}), "/clip?key=secret&user=a")
		if !strings.Contains(body, want) {
			t.Errorf("%v: body %q lacks %q", err, body, want)
		}
		if strings.Contains(body, "boom") {
			t.Errorf("internal error leaked to chat: %q", body)
		}
	}
}

func TestClipHostileUserName(t *testing.T) {
	f := &fakeClips{link: "https://clips.twitch.tv/x"}
	_, body := get(newTestServer(f), "/clip?key=secret&user=%2Fme%20ban%20all")
	if strings.Contains(body, "ban") {
		t.Errorf("unsanitised name echoed into chat: %q", body)
	}
}

func TestLimiterPerUserAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(10*time.Second, 30*time.Second)
	l.now = func() time.Time { return now }
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("first call refused")
	}
	now = now.Add(11 * time.Second)
	if ok, wait := l.allow("a"); ok || wait <= 0 {
		t.Fatalf("per-user cooldown not enforced: %v %v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("other viewer blocked after global window")
	}
	now = now.Add(31 * time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("cooldown never expires")
	}
}

type fakeTwitch struct {
	tokenCalls, createCalls, getCalls atomic.Int32
	createStatus                      int
	rejectFirstToken, neverReady      bool
}

func (f *fakeTwitch) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "rt" || r.Form.Get("client_secret") != "cs" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "expires_in": 3600})
	})
	mux.HandleFunc("POST /helix/clips", func(w http.ResponseWriter, r *http.Request) {
		n := f.createCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer at" || r.Header.Get("Client-Id") != "cid" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if f.rejectFirstToken && n == 1 {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		if f.createStatus != 0 {
			w.WriteHeader(f.createStatus)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "CLIP1"}}})
	})
	mux.HandleFunc("GET /helix/clips", func(w http.ResponseWriter, r *http.Request) {
		// Twitch returns an empty list until the clip finishes processing.
		if f.getCalls.Add(1) < 2 || f.neverReady {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"ID": "CLIP1"}}})
	})
	return mux
}

func testClient(t *testing.T, f *fakeTwitch) *twitchClient {
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c := newTwitchClient("cid", "cs", "rt", "123")
	c.authURL = srv.URL
	c.apiURL = srv.URL + "/helix"
	c.poll = 10 * time.Millisecond
	return c
}

func TestCreateClipWaitsUntilReady(t *testing.T) {
	f := &fakeTwitch{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link, err := testClient(t, f).createClip(ctx)
	if err != nil || link != "https://clips.twitch.tv/CLIP1" {
		t.Fatalf("got %q, %v", link, err)
	}
	if f.getCalls.Load() < 2 {
		t.Error("returned link before the clip was ready")
	}
}

func TestCreateClipRefreshesRejectedToken(t *testing.T) {
	f := &fakeTwitch{rejectFirstToken: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := testClient(t, f).createClip(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.tokenCalls.Load(); got != 2 {
		t.Errorf("token refreshed %d times, want 2", got)
	}
}

func TestCreateClipTokenIsCached(t *testing.T) {
	f := &fakeTwitch{}
	c := testClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range 2 {
		if _, err := c.createClip(ctx); err != nil {
			t.Fatal(err)
		}
		f.getCalls.Store(0)
	}
	if got := f.tokenCalls.Load(); got != 1 {
		t.Errorf("token fetched %d times, want 1", got)
	}
}

func TestCreateClipStatusMapping(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusNotFound:  errOffline,
		http.StatusForbidden: errClipsBlocked,
		http.StatusTeapot:    errUpstream,
	} {
		_, err := testClient(t, &fakeTwitch{createStatus: status}).createClip(context.Background())
		if !errors.Is(err, want) {
			t.Errorf("status %d: got %v, want %v", status, err, want)
		}
	}
}

func TestCreateClipGivesUpAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	f := &fakeTwitch{neverReady: true}
	if _, err := testClient(t, f).createClip(ctx); err == nil {
		t.Fatal("expected error when clip never becomes ready in time")
	}
}
