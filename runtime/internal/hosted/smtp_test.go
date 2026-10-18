package hosted

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func smtpReceiver(t *testing.T, implicit bool) (SMTPConfig, *tls.Config, <-chan string) {
	t.Helper()
	sample := httptest.NewTLSServer(http.NotFoundHandler())
	cert := sample.TLS.Certificates[0]
	sample.Close()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	messages := make(chan string, 2)
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				config := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
				if implicit {
					conn = tls.Server(conn, config)
				}
				reader := bufio.NewReader(conn)
				io.WriteString(conn, "220 localhost SMTP\r\n")
				secured := implicit
				for {
					line, e := reader.ReadString('\n')
					if e != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						if secured {
							io.WriteString(conn, "250-localhost\r\n250 8BITMIME\r\n")
						} else {
							io.WriteString(conn, "250-localhost\r\n250 STARTTLS\r\n")
						}
					case strings.HasPrefix(line, "STARTTLS"):
						io.WriteString(conn, "220 ready\r\n")
						conn = tls.Server(conn, config)
						reader = bufio.NewReader(conn)
						secured = true
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						io.WriteString(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "DATA"):
						io.WriteString(conn, "354 data\r\n")
						var body strings.Builder
						for {
							part, e := reader.ReadString('\n')
							if e != nil {
								return
							}
							if part == ".\r\n" {
								break
							}
							body.WriteString(part)
						}
						messages <- body.String()
						io.WriteString(conn, "250 accepted\r\n")
					case strings.HasPrefix(line, "QUIT"):
						io.WriteString(conn, "221 bye\r\n")
						return
					default:
						io.WriteString(conn, "500 unexpected\r\n")
					}
				}
			}(conn)
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	mode := "starttls"
	if implicit {
		mode = "tls"
	}
	return SMTPConfig{Host: "127.0.0.1", Port: n, TLSMode: mode, From: "wia@example.com"}, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}, messages
}

func TestSMTPUsesVerifiedTLSAndDeliversChineseMail(t *testing.T) {
	for _, implicit := range []bool{true, false} {
		t.Run(strconv.FormatBool(implicit), func(t *testing.T) {
			config, roots, messages := smtpReceiver(t, implicit)
			sender := &SMTPMail{Config: config, tlsConfig: roots}
			if err := sender.Send(t.Context(), MailMessage{To: "player@example.com", Subject: "邮箱验证", Body: "验证码：123456\n仅供本地测试"}); err != nil {
				t.Fatal(err)
			}
			select {
			case body := <-messages:
				if !strings.Contains(body, "123456") || !strings.Contains(body, "charset=UTF-8") {
					t.Fatal("mail body missing")
				}
			case <-time.After(time.Second):
				t.Fatal("no message")
			}
			sender.tlsConfig = nil
			if err := sender.Send(t.Context(), MailMessage{To: "player@example.com", Subject: "test", Body: "should fail"}); err == nil {
				t.Fatal("untrusted certificate accepted")
			}
		})
	}
}

func TestSMTPCancellationInterruptsPendingGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			defer conn.Close()
			io.Copy(io.Discard, conn)
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	sender := &SMTPMail{Config: SMTPConfig{Host: "127.0.0.1", Port: n, TLSMode: "starttls", From: "wia@example.com"}}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = sender.Send(ctx, MailMessage{To: "player@example.com", Subject: "test"})
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("cancelled SMTP request kept waiting")
	}
}
