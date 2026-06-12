package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mqtik/mate-relay/internal/admin"
	"github.com/mqtik/mate-relay/internal/adminui"
	"github.com/mqtik/mate-relay/internal/auth"
	"github.com/mqtik/mate-relay/internal/config"
	"github.com/mqtik/mate-relay/internal/sni"
	"github.com/mqtik/mate-relay/internal/storage"
	"github.com/mqtik/mate-relay/internal/tunnel"
	"golang.org/x/crypto/acme/autocert"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		log.Fatalf("mkdir data: %v", err)
	}
	if err := os.MkdirAll(cfg.CertDir, 0700); err != nil {
		log.Fatalf("mkdir certs: %v", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "relay.db")
	db, err := storage.Open(dbPath, cfg.CodeHashPepper, cfg.DeviceTokenSecret)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	registry := tunnel.NewRegistry()
	tunnelHandler := tunnel.NewHandler(registry, db)
	adminHandler := admin.NewHandler(db, registry, cfg.ControlHost)

	mux := http.NewServeMux()

	mux.Handle("POST /redeem", http.HandlerFunc(adminHandler.Redeem))

	mux.Handle("GET /tunnel/control",
		auth.DeviceMiddleware(db, http.HandlerFunc(tunnelHandler.ServeControl)),
	)
	mux.Handle("GET /tunnel/stream",
		http.HandlerFunc(tunnelHandler.ServeStream),
	)

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /admin/codes", adminHandler.ListCodes)
	adminMux.HandleFunc("POST /admin/codes", adminHandler.CreateCode)
	adminMux.HandleFunc("DELETE /admin/codes/{id}", adminHandler.RevokeCode)
	adminMux.HandleFunc("DELETE /admin/devices/{id}", adminHandler.RevokeDevice)

	mux.Handle("/admin/", auth.AdminMiddleware(cfg.AdminBearerSecret, adminMux))

	if cfg.AdminUIPassword != "" {
		ui := adminui.NewHandler(db, registry, cfg.AdminUIPassword)
		ui.Mount(mux)
		log.Printf("Admin UI enabled at /add")
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true}`)
	})

	var tlsCfg *tls.Config
	if cfg.Dev {
		tlsCfg, err = selfSignedTLS(cfg.ControlHost)
		if err != nil {
			log.Fatalf("self-signed cert: %v", err)
		}
		log.Printf("DEV mode: using self-signed TLS for %s", cfg.ControlHost)
	} else {
		certManager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.ControlHost),
			Cache:      autocert.DirCache(cfg.CertDir),
			Email:      cfg.LEEmail,
		}
		tlsCfg = certManager.TLSConfig()
		go func() {
			log.Printf("HTTP ACME challenge server on %s", cfg.HTTPAddr)
			if err := http.ListenAndServe(cfg.HTTPAddr, certManager.HTTPHandler(nil)); err != nil {
				log.Printf("ACME HTTP server: %v", err)
			}
		}()
	}

	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.ListenAddr, err)
	}
	log.Printf("Listening on %s (control host: %s)", cfg.ListenAddr, cfg.ControlHost)

	demux := &sni.Demux{
		ControlHost: cfg.ControlHost,
		TLSConfig:   tlsCfg,
		OnControl: func(conn net.Conn) {
			httpServer.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
				return ctx
			}
			go func() {
				defer conn.Close()
				srv := &http.Server{
					Handler:      mux,
					ReadTimeout:  30 * time.Second,
					WriteTimeout: 0,
					IdleTimeout:  120 * time.Second,
				}
				srv.Serve(newOneConnListener(conn))
			}()
		},
		OnTunnel: func(sniHost string, conn net.Conn) {
			macID := extractMacID(sniHost, cfg.ControlHost)
			if macID == "" {
				conn.Close()
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := tunnel.Proxy(ctx, registry, macID, conn, 15*time.Second); err != nil {
				log.Printf("proxy %s -> mac %s: %v", sniHost, macID, err)
				conn.Close()
			}
		},
	}

	log.Fatal(demux.Serve(ln))
}

func extractMacID(sniHost, controlHost string) string {
	suffix := "." + controlHost
	if len(sniHost) > len(suffix) && sniHost[len(sniHost)-len(suffix):] == suffix {
		return sniHost[:len(sniHost)-len(suffix)]
	}
	return ""
}

type oneConnListener struct {
	conn net.Conn
	ch   chan net.Conn
}

func newOneConnListener(conn net.Conn) *oneConnListener {
	ch := make(chan net.Conn, 1)
	ch <- conn
	return &oneConnListener{conn: conn, ch: ch}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	conn, ok := <-l.ch
	if !ok {
		return nil, fmt.Errorf("listener closed")
	}
	return conn, nil
}

func (l *oneConnListener) Close() error {
	return nil
}

func (l *oneConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func selfSignedTLS(host string) (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host, "*." + host},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
