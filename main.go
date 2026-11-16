// Command bedit is a Google Docs-style document viewer (and, in later
// steps, collaborative editor) that runs behind bserver.
//
// bserver forwards /bedit/... to it, either over TCP (bserver's existing
// proxy-path setting, today) or over a unix socket when bserver starts it
// on demand (planned). Both are just a listener here, so bedit doesn't care
// which. All state is on disk under the storage root, so bedit can be
// stopped and restarted at any time without losing anything.
//
// Settings come from flags, each defaulting to an environment variable so
// a spawning parent can configure bedit without building a command line:
//
//	-listen  BEDIT_LISTEN  TCP address (default 127.0.0.1:8090)
//	-socket  BEDIT_SOCKET  unix socket path; overrides -listen when set
//	-root    BEDIT_ROOT    storage root (default: current directory, which
//	                       is the site's bedit/ directory under bserver)
//	-prefix  BEDIT_PREFIX  URL path bedit is mounted at (default /bedit)
package main

import (
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/stgnet/bedit/internal/server"
	"github.com/stgnet/bedit/internal/store"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", env("BEDIT_LISTEN", "127.0.0.1:8090"), "TCP address to listen on")
	socket := flag.String("socket", env("BEDIT_SOCKET", ""), "unix socket to listen on instead of TCP")
	root := flag.String("root", env("BEDIT_ROOT", "."), "storage root directory")
	prefix := flag.String("prefix", env("BEDIT_PREFIX", "/bedit"), "URL path prefix")
	flag.Parse()

	if fi, err := os.Stat(*root); err != nil || !fi.IsDir() {
		log.Fatalf("storage root %q is not a directory", *root)
	}

	var ln net.Listener
	var err error
	if *socket != "" {
		// A socket file left over from a previous run would make Listen
		// fail; nothing else can be using it if we were asked to own it.
		if err := os.Remove(*socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Fatal(err)
		}
		ln, err = net.Listen("unix", *socket)
	} else {
		ln, err = net.Listen("tcp", *listen)
	}
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Handler:           server.New(store.New(*root), *prefix).Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}
	log.Printf("bedit serving %s on %s, storage in %s", *prefix, ln.Addr(), *root)
	log.Fatal(srv.Serve(ln))
}
