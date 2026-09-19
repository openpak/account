package mailer

import (
	"bufio"
	"mime"
	"net"
	"net/textproto"
	"strings"
	"testing"
)

// fakeSMTP accepts one message (no TLS, no auth) and returns its envelope sender and data.
func fakeSMTP(t *testing.T) (port int, got chan [2]string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan [2]string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		tp.PrintfLine("220 fake")
		var from string
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(line); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				tp.PrintfLine("250 fake")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				from = strings.Trim(line[10:], "<>")
				tp.PrintfLine("250 ok")
			case strings.HasPrefix(cmd, "RCPT"):
				tp.PrintfLine("250 ok")
			case cmd == "DATA":
				tp.PrintfLine("354 go")
				data, _ := tp.ReadDotBytes()
				tp.PrintfLine("250 ok")
				got <- [2]string{from, string(data)}
			case cmd == "QUIT":
				tp.PrintfLine("221 bye")
				return
			default:
				tp.PrintfLine("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestSendHeaders(t *testing.T) {
	port, got := fakeSMTP(t)
	m := New(Config{Host: "127.0.0.1", Port: port, From: "OpenPak <noreply@openpak.org>"})
	if err := m.Send("a@x.test", "Setze dein OpenPak-Passwort zurück", "Hallo"); err != nil {
		t.Fatal(err)
	}
	msg := <-got
	if msg[0] != "noreply@openpak.org" {
		t.Errorf("envelope from %q", msg[0])
	}
	h, err := textproto.NewReader(bufio.NewReader(strings.NewReader(msg[1]))).ReadMIMEHeader()
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
	if subj != "Setze dein OpenPak-Passwort zurück" || strings.ContainsAny(h.Get("Subject"), "ü") {
		t.Errorf("subject %q (raw %q)", subj, h.Get("Subject"))
	}
	if !strings.HasSuffix(h.Get("Message-Id"), "@openpak.org>") || h.Get("From") != `"OpenPak" <noreply@openpak.org>` {
		t.Errorf("headers %v", h)
	}
}
