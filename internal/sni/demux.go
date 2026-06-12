package sni

import (
	"crypto/tls"
	"log"
	"net"
	"time"

	vhost "github.com/inconshreveable/go-vhost"
)

type Demux struct {
	ControlHost string
	TLSConfig   *tls.Config
	OnControl   func(net.Conn)
	OnTunnel    func(sni string, conn net.Conn)
}

func (d *Demux) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go d.handle(conn)
	}
}

func (d *Demux) handle(raw net.Conn) {
	raw.SetDeadline(time.Now().Add(10 * time.Second))

	tlsConn, err := vhost.TLS(raw)
	if err != nil {
		log.Printf("sni: vhost.TLS: %v", err)
		raw.Close()
		return
	}

	sni := tlsConn.Host()
	raw.SetDeadline(time.Time{})

	switch {
	case sni == d.ControlHost || sni == "":
		if d.TLSConfig != nil {
			upgraded := tls.Server(tlsConn, d.TLSConfig)
			if d.OnControl != nil {
				d.OnControl(upgraded)
			} else {
				upgraded.Close()
			}
		} else {
			if d.OnControl != nil {
				d.OnControl(tlsConn)
			} else {
				tlsConn.Close()
			}
		}
	default:
		if d.OnTunnel != nil {
			d.OnTunnel(sni, tlsConn)
		} else {
			tlsConn.Close()
		}
	}
}
