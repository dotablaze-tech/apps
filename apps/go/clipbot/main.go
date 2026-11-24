package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"
)

// StreamElements aborts customapi calls after 15s; leave headroom for the reply.
const clipBudget = 12 * time.Second

var viewerName = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)

type clipMaker interface {
	createClip(ctx context.Context) (string, error)
}

type server struct {
	key     string
	clips   clipMaker
	limiter *limiter
	log     *slog.Logger
}

// Replies are plain 200 text: chat bots print the body whatever the status,
// and a non-2xx makes some of them show their own generic error instead.
func (s *server) clip(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(s.key)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	user := r.URL.Query().Get("user")
	if !viewerName.MatchString(user) {
		user = "anonymous"
	}
	if ok, wait := s.limiter.allow(user); !ok {
		fmt.Fprintf(w, "Clip on cooldown, try again in %ds.", int(wait.Seconds())+1)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clipBudget)
	defer cancel()
	link, err := s.clips.createClip(ctx)
	switch {
	case err == nil:
		s.log.Info("clip created", "user", user, "link", link)
		fmt.Fprintf(w, "Clip by %s: %s", user, link)
	case errors.Is(err, errOffline):
		fmt.Fprint(w, "Can't clip: the stream is offline.")
	case errors.Is(err, errClipsBlocked):
		fmt.Fprint(w, "Can't clip: clipping is disabled for this channel.")
	default:
		s.log.Error("clip failed", "user", user, "error", err)
		fmt.Fprint(w, "Clip failed, try again in a moment.")
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /clip", s.clip)
	ok := func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }
	mux.HandleFunc("GET /liveness", ok)
	mux.HandleFunc("GET /readiness", ok)
	return mux
}

func must(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "missing required env %s\n", name)
		os.Exit(2)
	}
	return v
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := &http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://127.0.0.1:" + port + "/liveness")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &server{
		key:     must("CLIP_KEY"),
		clips:   newTwitchClient(must("TWITCH_CLIENT_ID"), must("TWITCH_CLIENT_SECRET"), must("TWITCH_REFRESH_TOKEN"), must("TWITCH_BROADCASTER_ID")),
		limiter: newLimiter(10*time.Second, 30*time.Second),
		log:     log,
	}
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      clipBudget + 3*time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("clipbot listening", "port", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
