package transports

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// A mail server that accepts the connection and never answers must not block a send.
func TestSendMailHungServerTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	saved := client
	client = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { client = saved }()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	done := make(chan error, 1)
	go func() { done <- sendMail(host, port, "a@x", []string{"b@x"}, []byte("hi"), "", "", false) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a hung server")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sendMail blocked on a hung server")
	}
}
