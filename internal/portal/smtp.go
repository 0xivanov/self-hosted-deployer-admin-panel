package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
	"unicode"
)

type SMTPOptions struct {
	Address     string `json:"address"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	From        string `json:"from"`
	ImplicitTLS bool   `json:"implicit_tls"`
	ServerName  string `json:"server_name,omitempty"`
}
type SMTPSender struct {
	options    SMTPOptions
	host       string
	serverName string
	tlsConfig  *tls.Config
}

func NewSMTPSender(opts SMTPOptions) (*SMTPSender, error) {
	host, port, err := net.SplitHostPort(opts.Address)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("SMTP address must include host and port")
	}
	from, err := mail.ParseAddress(opts.From)
	if err != nil || from.Address != opts.From || strings.ContainsAny(opts.From, "\r\n") {
		return nil, errors.New("SMTP from must be a plain email address")
	}
	if (opts.Username == "") != (opts.Password == "") {
		return nil, errors.New("SMTP username and password must be supplied together")
	}
	serverName := host
	if opts.ServerName != "" {
		if err := validateSMTPServerName(opts.ServerName); err != nil {
			return nil, err
		}
		serverName = opts.ServerName
	}
	return &SMTPSender{options: opts, host: host, serverName: serverName, tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}}, nil
}

func validateSMTPServerName(serverName string) error {
	if serverName == "" || strings.TrimSpace(serverName) != serverName || strings.IndexFunc(serverName, unicode.IsSpace) >= 0 {
		return errors.New("SMTP server_name must be a hostname")
	}
	if net.ParseIP(serverName) != nil {
		return nil
	}
	if len(serverName) > 253 || strings.ContainsAny(serverName, "/\\:@") || strings.HasSuffix(serverName, ".") {
		return errors.New("SMTP server_name must be a hostname")
	}
	for _, label := range strings.Split(serverName, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("SMTP server_name must be a hostname")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return errors.New("SMTP server_name must be a hostname")
			}
		}
	}
	return nil
}

// Send requires verified TLS, either implicit or STARTTLS. It never falls back
// to cleartext, including anonymous SMTP, and never returns provider text.
func (s *SMTPSender) Send(ctx context.Context, message Mail) error {
	to, err := mail.ParseAddress(message.To)
	if err != nil || to.Address != message.To || strings.ContainsAny(message.To+message.Subject+message.ID, "\r\n") {
		return errors.New("invalid mail envelope")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.options.Address)
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer raw.Close()
	deadline, _ := ctx.Deadline()
	raw.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	var conn net.Conn = raw
	if s.options.ImplicitTLS {
		secure := tls.Client(raw, s.tlsConfig.Clone())
		if err = secure.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS handshake failed")
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, s.serverName)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if !s.options.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP requires STARTTLS")
		}
		if err = client.StartTLS(s.tlsConfig.Clone()); err != nil {
			return errors.New("SMTP TLS handshake failed")
		}
	}
	if s.options.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.options.Username, s.options.Password, s.serverName)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err = client.Mail(s.options.From); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err = client.Rcpt(message.To); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message rejected")
	}
	body := strings.ReplaceAll(strings.ReplaceAll(message.Text, "\r\n", "\n"), "\n", "\r\n")
	_, err = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", s.options.From, message.To, message.Subject, message.ID, s.host, body)
	if err != nil {
		return errors.New("SMTP message write failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("SMTP message not accepted")
	}
	// DATA acceptance is the delivery boundary. A later QUIT failure must not
	// turn an accepted message into an immediate retry.
	client.Quit()
	return nil
}

// Run delivers queued mail until cancellation. Reporter receives only sanitized
// errors. SMTP can duplicate an accepted message after a worker crash.
func (m *AccountMail) Run(ctx context.Context, sender MailSender, report func(error)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			worked, err := m.DeliverOne(ctx, sender)
			if ctx.Err() != nil {
				return
			}
			if err != nil && report != nil {
				report(err)
			}
			delay := 5 * time.Second
			if worked && err == nil {
				delay = 100 * time.Millisecond
			}
			timer.Reset(delay)
		}
	}
}
