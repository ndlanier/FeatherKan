package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	store  *Store
	token  string
	mcp    bool
	origin string
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, map[string]any{"mcp": s.mcp}) })
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.store.run("list_tasks", Input{Query: r.URL.Query().Get("q")})
		s.respond(w, v, e)
	})
	mux.HandleFunc("POST /api/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in Input
		if decode(w, r, &in) != nil {
			jsonReply(w, 400, map[string]string{"error": "Invalid request body"})
			return
		}
		v, e := s.store.run(r.PathValue("action"), in)
		s.respond(w, v, e)
	})
	mux.HandleFunc("/mcp", s.handleMCP)
	sub, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if o := r.Header.Get("Origin"); o != "" {
			u, e := url.Parse(o)
			if e != nil || ((u.Scheme != "http" && u.Scheme != "https") || u.Host != r.Host) && o != s.origin {
				http.Error(w, "Origin not allowed", 403)
				return
			}
		}
		// Without a token, accept only loopback Host values, including inside Docker.
		if s.token == "" {
			host := r.Host
			if h, _, e := net.SplitHostPort(host); e == nil {
				host = h
			}
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				http.Error(w, "Set BOARD_TOKEN for remote access", 403)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp" {
			if s.token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
				jsonReply(w, 401, map[string]string{"error": "Enter the board access token to connect."})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		jsonReply(w, 200, v)
		return
	}
	code := 500
	msg := "Could not save the board"
	var a *apiError
	if errors.As(e, &a) {
		code = a.Code
		msg = a.Message
	} else {
		log.Printf("board operation: %v", e)
	}
	jsonReply(w, code, map[string]string{"error": msg})
}
func main() {
	addr := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	dir := flag.String("data", "./data", "Persistent data directory")
	mcp := flag.Bool("mcp", false, "Enable optional MCP endpoint at /mcp")
	origin := flag.String("origin", "", "Additional allowed browser origin, e.g. https://board.example.com")
	flag.Parse()
	token := os.Getenv("BOARD_TOKEN")
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(host)
	if token == "" && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Fatal("BOARD_TOKEN is required when listening beyond loopback")
	}
	store, err := openStore(*dir)
	if err != nil {
		log.Fatal(err)
	}
	defer store.close()
	app := &Server{store: store, token: token, mcp: *mcp, origin: *origin}
	srv := &http.Server{Addr: *addr, Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	log.Printf("FeatherKan listening on %s (MCP enabled: %t)", *addr, *mcp)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
