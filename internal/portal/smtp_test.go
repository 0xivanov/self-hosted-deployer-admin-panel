package portal

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSMTPRequiresVerifiedTLS(t *testing.T) {
	t.Parallel()
	for _, secure := range []bool{false, true} {
		name := "reject plaintext"
		if secure {
			name = "deliver verified TLS"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var roots *x509.CertPool
			if secure {
				fixture := httptest.NewTLSServer(nil)
				cfg := fixture.TLS.Clone()
				fixture.Close()
				listener = tls.NewListener(listener, cfg)
				cert, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				roots = x509.NewCertPool()
				roots.AddCert(cert)
			}
			received := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					received <- ""
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 test ready\r\n")
				var body strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						received <- body.String()
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250 test\r\n")
					case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
						fmt.Fprint(conn, "250 OK\r\n")
					case line == "DATA\r\n":
						fmt.Fprint(conn, "354 data\r\n")
						for {
							line, err = reader.ReadString('\n')
							if err != nil {
								received <- body.String()
								return
							}
							if line == ".\r\n" {
								break
							}
							body.WriteString(line)
						}
						fmt.Fprint(conn, "250 accepted\r\n")
					case line == "QUIT\r\n":
						fmt.Fprint(conn, "221 bye\r\n")
						received <- body.String()
						return
					default:
						fmt.Fprint(conn, "500 unsupported\r\n")
					}
				}
			}()
			sender, err := NewSMTPSender(SMTPOptions{Address: listener.Addr().String(), From: "sender@example.test", ImplicitTLS: secure})
			if err != nil {
				t.Fatal(err)
			}
			sender.tlsConfig.RootCAs = roots
			err = sender.Send(context.Background(), Mail{ID: "test-message", To: "recipient@example.test", Subject: "Verification", Text: "synthetic test only"})
			body := <-received
			if secure {
				if err != nil || !strings.Contains(body, "synthetic test only") {
					t.Fatalf("TLS delivery: %v %q", err, body)
				}
			} else if err == nil || body != "" {
				t.Fatal("plaintext accepted", err, body)
			}
		})
	}
}
func TestSMTPRejectsInjectedHeaders(t *testing.T) {
	sender, err := NewSMTPSender(SMTPOptions{Address: "127.0.0.1:1", From: "sender@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Mail{To: "recipient@example.test", Subject: "hello\r\nBcc: hidden@example.test"})
	if err == nil || err.Error() != "invalid mail envelope" {
		t.Fatal(err)
	}
}
