// Command flow-notes-local is the on-your-machine half of Flow Notes.
//
// The hosted backend cannot reach a user's filesystem or Notes.app, so
// notes with storage_target "apple_notes" or "local" are written by this
// small process instead.
//
// Chrome launches it as a NATIVE MESSAGING HOST and talks to it over
// stdin/stdout — no port, no CORS, and nothing for the user to start. The
// -serve flag runs an HTTP server on loopback instead, which exists purely
// so the helper can be driven with curl during development.
//
// Stdlib only: this is something a user installs, so it should build and
// run with nothing to fetch.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Version is reported by the health method so the extension can tell
// whether the helper it is talking to predates a feature it needs.
// Bump it whenever the method set changes, and raise HELPER_MIN_VERSION
// in the extension's background.js to match.
const Version = "0.5.0"

const (
	defaultPort    = "4500"
	defaultDirName = "FlowNotes"

	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 15 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func main() {
	var (
		serveFlag  = flag.Bool("serve", false, "run the development HTTP server instead of speaking native messaging")
		dirFlag    = flag.String("dir", envOr("FLOW_NOTES_DIR", ""), "directory to store note files in (default ~/FlowNotes)")
		portFlag   = flag.String("port", envOr("FLOW_NOTES_PORT", defaultPort), "-serve only: port to listen on (loopback)")
		originFlag = flag.String("origin", envOr("FLOW_NOTES_ALLOWED_ORIGIN", ""), "-serve only: browser origin allowed to call the dev server")
		appleFlag  = flag.String("apple-folder", envOr("FLOW_NOTES_APPLE_FOLDER", "Flow Notes"), "Notes.app folder for new Apple Notes (empty for the default folder)")
	)
	flag.Parse()

	// Chrome passes the calling extension's origin as an argument. It is
	// not used for access control — the host manifest's allowed_origins is
	// what Chrome enforces — but seeing it in the log confirms who called.
	if !*serveFlag && flag.NArg() > 0 {
		log.Printf("launched by %s", flag.Arg(0))
	}

	dir := *dirFlag
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("could not determine home directory; pass -dir explicitly: %v", err)
		}
		dir = filepath.Join(home, defaultDirName)
	}

	store, err := NewStore(dir)
	if err != nil {
		log.Fatalf("failed to open notes directory: %v", err)
	}

	apple := NewAppleNotesStore(*appleFlag)
	if err := apple.Available(); err != nil {
		log.Printf("Apple Notes target unavailable: %v", err)
	}
	api := NewAPI(store, apple)

	if *serveFlag {
		serveHTTP(api, *portFlag, *originFlag, store.Dir())
		return
	}

	// Native messaging. Everything diagnostic goes to stderr: stdout is
	// the wire, and a stray byte there corrupts the framing and hangs the
	// channel.
	log.SetOutput(os.Stderr)
	log.SetPrefix("flow-notes-local: ")
	log.Printf("native messaging host %s ready (notes: %s)", Version, store.Dir())

	if err := ServeNativeMessaging(api, os.Stdin, os.Stdout); err != nil {
		log.Printf("native messaging ended: %v", err)
		os.Exit(1)
	}
}

func serveHTTP(api *API, port, origin, notesDir string) {
	if origin == "" {
		log.Printf("WARNING: -origin not set — allowing any chrome-extension:// origin")
	}

	srv := &http.Server{
		// Loopback only. Binding 0.0.0.0 would expose the user's notes
		// directory to anything else on their network.
		Addr:              "127.0.0.1:" + port,
		Handler:           withCORS(origin, routes(api)),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("flow-notes local server %s (dev mode) on http://%s", Version, srv.Addr)
		log.Printf("notes directory: %s", notesDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		log.Fatalf("server failed: %v", err)
	case <-ctx.Done():
	}

	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Printf("stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
