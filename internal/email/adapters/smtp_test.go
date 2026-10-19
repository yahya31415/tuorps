package adapters_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/email/adapters"
	"github.com/yahya31415/tuorps/internal/platform/config"
	"github.com/yahya31415/tuorps/internal/platform/testinfra"
)

// selfSigned returns a PEM certificate and key for host plus a pool trusting it.
func selfSigned(t *testing.T, host string) (certPEM, keyPEM []byte, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), pool
}

func message(subject string) []byte {
	return []byte("From: Tuorps <no-reply@tuorps.in>\r\nTo: priya@example.in\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhello\r\n")
}

func env() adapters.Envelope {
	return adapters.Envelope{From: "no-reply@tuorps.in", To: "priya@example.in"}
}

func smtpConfig(m *testinfra.Mailpit, mode config.SMTPTLS, user string) config.SMTP {
	c := config.SMTP{
		Host:    m.SMTPHost,
		Port:    m.SMTPPort,
		TLS:     mode,
		From:    mail.Address{Address: "no-reply@tuorps.in"},
		Timeout: 5 * time.Second,
	}
	if user != "" {
		c.Username, c.Password = user, config.NewSecret("pw")
	}
	return c
}

// subjects lists the subjects Mailpit has received.
func subjects(t *testing.T, m *testinfra.Mailpit) []string {
	t.Helper()
	resp, err := http.Get(m.APIURL + "/api/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Messages []struct {
			Subject string
			To      []struct{ Address string }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, msg := range body.Messages {
		if len(msg.To) != 1 || msg.To[0].Address != "priya@example.in" {
			t.Errorf("unexpected recipients %+v", msg.To)
		}
		out = append(out, msg.Subject)
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestSMTPPlaintextForDev(t *testing.T) {
	m := testinfra.StartMailpit(t, testinfra.MailpitOptions{})
	s := adapters.NewSMTP(smtpConfig(m, config.SMTPNoTLS, ""), nil)
	if err := s.Send(t.Context(), env(), message("plain")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !has(subjects(t, m), "plain") {
		t.Fatal("message not delivered")
	}
}

func TestSMTPRefusesToDowngradeWithoutSTARTTLS(t *testing.T) {
	m := testinfra.StartMailpit(t, testinfra.MailpitOptions{})
	s := adapters.NewSMTP(smtpConfig(m, config.SMTPStartTLS, "user"), nil)
	err := s.Send(t.Context(), env(), message("downgrade"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want refusal without STARTTLS", err)
	}
	if has(subjects(t, m), "downgrade") {
		t.Fatal("message sent in plaintext")
	}
}

func TestSMTPStartTLS(t *testing.T) {
	cert, key, pool := selfSigned(t, "mailpit.test")
	m := testinfra.StartMailpit(t, testinfra.MailpitOptions{CertPEM: cert, KeyPEM: key, RequireSTARTTLS: true})

	t.Run("trusted certificate with authentication", func(t *testing.T) {
		s := adapters.NewSMTP(smtpConfig(m, config.SMTPStartTLS, "user"), &tls.Config{RootCAs: pool, ServerName: "mailpit.test", MinVersion: tls.VersionTLS12})
		if err := s.Send(t.Context(), env(), message("starttls")); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if !has(subjects(t, m), "starttls") {
			t.Fatal("not delivered")
		}
	})
	t.Run("untrusted certificate is refused", func(t *testing.T) {
		s := adapters.NewSMTP(smtpConfig(m, config.SMTPStartTLS, "user"), nil) // system roots
		if err := s.Send(t.Context(), env(), message("untrusted")); err == nil {
			t.Fatal("sent over an untrusted certificate")
		}
		if has(subjects(t, m), "untrusted") {
			t.Fatal("message delivered despite the certificate error")
		}
	})
}

func TestSMTPImplicitTLS(t *testing.T) {
	cert, key, pool := selfSigned(t, "mailpit.test")
	m := testinfra.StartMailpit(t, testinfra.MailpitOptions{CertPEM: cert, KeyPEM: key, ImplicitTLS: true})
	s := adapters.NewSMTP(smtpConfig(m, config.SMTPImplicitTLS, "user"), &tls.Config{RootCAs: pool, ServerName: "mailpit.test", MinVersion: tls.VersionTLS12})
	if err := s.Send(t.Context(), env(), message("implicit")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !has(subjects(t, m), "implicit") {
		t.Fatal("not delivered")
	}
}

func TestSMTPTimesOutOnASilentServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }() // accept and say nothing
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	s := adapters.NewSMTP(config.SMTP{Host: "127.0.0.1", Port: addr.Port, TLS: config.SMTPNoTLS, Timeout: 300 * time.Millisecond}, nil)
	start := time.Now()
	if err := s.Send(t.Context(), env(), message("x")); err == nil {
		t.Fatal("want timeout")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestSMTPHonoursContextCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer func() { _ = c.Close() }()
			time.Sleep(5 * time.Second)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	s := adapters.NewSMTP(config.SMTP{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, TLS: config.SMTPNoTLS, Timeout: time.Minute}, nil)
	start := time.Now()
	if err := s.Send(ctx, env(), message("x")); err == nil {
		t.Fatal("want cancellation error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v despite cancellation", d)
	}
}

func TestSMTPRejectsHeaderInjectionInEnvelope(t *testing.T) {
	s := adapters.NewSMTP(config.SMTP{Host: "127.0.0.1", Port: 1, TLS: config.SMTPNoTLS, Timeout: time.Second}, nil)
	for _, e := range []adapters.Envelope{
		{From: "a@b.in\r\nRCPT TO:<evil@x.in>", To: "p@example.in"},
		{From: "a@b.in", To: "p@example.in\r\nDATA"},
	} {
		if err := s.Send(t.Context(), e, message("x")); err == nil || !strings.Contains(err.Error(), "envelope") {
			t.Errorf("envelope %q: err = %v", fmt.Sprint(e), err)
		}
	}
}
