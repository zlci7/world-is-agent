package hosted

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrMailNotConfigured = errors.New("mail service is not configured")

type SMTPConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	TLSMode     string `json:"tls_mode"`
	From        string `json:"from"`
	Username    string `json:"username"`
	PasswordEnv string `json:"password_env"`
}

func (c SMTPConfig) Validate() error {
	if c == (SMTPConfig{}) {
		return nil
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil || from.Address != c.From || c.Host == "" || strings.ContainsAny(c.Host, "\r\n/: ") || c.Port < 1 || c.Port > 65535 || (c.TLSMode != "tls" && c.TLSMode != "starttls") || (c.Username != "" && c.PasswordEnv == "") {
		return errors.New("invalid SMTP configuration")
	}
	return nil
}

type MailMessage struct{ To, Subject, Body string }
type MailSender interface {
	Send(context.Context, MailMessage) error
}
type SMTPMail struct {
	Config    SMTPConfig
	tlsConfig *tls.Config
}

func (m *SMTPMail) Send(ctx context.Context, message MailMessage) error {
	c := m.Config
	if c.Host == "" {
		return ErrMailNotConfigured
	}
	if err := c.Validate(); err != nil {
		return err
	}
	recipient, err := mail.ParseAddress(message.To)
	if err != nil || recipient.Address != message.To || strings.ContainsAny(message.Subject, "\r\n") {
		return errors.New("invalid mail message")
	}
	password := os.Getenv(c.PasswordEnv)
	if c.Username != "" && password == "" {
		return ErrMailNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	config := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if m.tlsConfig != nil {
		config = m.tlsConfig.Clone()
	}
	if c.TLSMode == "tls" {
		secured := tls.Client(conn, config)
		if err = secured.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server requires STARTTLS support")
		}
		if err = client.StartTLS(config); err != nil {
			return err
		}
	}
	if c.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.Username, password, c.Host)); err != nil {
			return err
		}
	}
	if err = client.Mail(c.From); err != nil {
		return err
	}
	if err = client.Rcpt(message.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	from := (&mail.Address{Name: "World Is Agent", Address: c.From}).String()
	subject := mimeSubject(message.Subject)
	body := strings.ReplaceAll(strings.ReplaceAll(message.Body, "\r\n", "\n"), "\n", "\r\n")
	if _, err = io.WriteString(writer, fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", from, recipient.String(), subject, body)); err != nil {
		return err
	}
	// DATA's final response confirms acceptance; QUIT does not change that result.
	if err = writer.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
