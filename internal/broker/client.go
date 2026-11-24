// Package broker talks to the Kafka broker over TLS and SASL/SCRAM, using only the Kafka protocol.
package broker

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// Connection says how to reach the broker and who to be.
type Connection struct {
	Address    string
	CACertPath string
	User       string
	Password   string
}

// NewClient connects lazily; the first request opens the TLS connection.
func NewClient(conn Connection, extraOpts ...kgo.Opt) (*kgo.Client, error) {
	caPEM, err := os.ReadFile(conn.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s contains no certificate", conn.CACertPath)
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(conn.Address),
		kgo.DialTLSConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}),
		kgo.SASL(scram.Auth{User: conn.User, Pass: conn.Password}.AsSha256Mechanism()),
	}
	return kgo.NewClient(append(opts, extraOpts...)...)
}

// NewAdmin is NewClient wrapped for topic, ACL and SCRAM user management. Closing it closes the client.
func NewAdmin(conn Connection) (*kadm.Client, error) {
	client, err := NewClient(conn)
	if err != nil {
		return nil, err
	}
	return kadm.NewClient(client), nil
}
