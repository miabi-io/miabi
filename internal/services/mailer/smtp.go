// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailer

import (
	"crypto/tls"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/config"
)

// sendMail delivers an HTML message to the recipients over the configured system
// SMTP server, honoring the encryption mode (none | starttls | ssl/tls).
func sendMail(cfg config.SystemSMTPConfig, to []string, subject, htmlBody string) error {
	host := cfg.Host
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Port))
	msg := buildMessage(cfg.From, to, subject, htmlBody)

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, host)
	}
	from := envelopeAddr(cfg.From)

	switch strings.ToLower(strings.TrimSpace(cfg.Encryption)) {
	case "ssl", "tls":
		return sendImplicitTLS(addr, host, auth, from, to, msg)
	case "starttls":
		return sendStartTLS(addr, host, auth, from, to, msg)
	default: // none / plaintext
		return smtp.SendMail(addr, auth, from, to, msg)
	}
}

func sendImplicitTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return deliver(c, auth, from, to, msg)
}

func sendStartTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	return deliver(c, auth, from, to, msg)
}

func deliver(c *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func buildMessage(from string, to []string, subject, htmlBody string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

// envelopeAddr extracts the bare address from an RFC 5322 string such as
// "Miabi <noreply@example.com>", returning just "noreply@example.com". On parse
// failure it returns the input unchanged.
func envelopeAddr(addr string) string {
	if a, err := mail.ParseAddress(addr); err == nil {
		return a.Address
	}
	return addr
}
