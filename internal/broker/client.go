// Package broker talks to the Kafka broker over TLS and SASL/SCRAM, using only the Kafka protocol.
package broker

import (
	"context"
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

// Admin manages topics, ACLs and SCRAM users and publishes records, as the principal it connected with.
// Closing it closes the connection.
type Admin struct {
	*kadm.Client
	kafka *kgo.Client
}

// NewAdmin connects lazily; the first request opens the TLS connection.
func NewAdmin(conn Connection) (*Admin, error) {
	caPEM, err := os.ReadFile(conn.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%s contains no certificate", conn.CACertPath)
	}
	kafka, err := kgo.NewClient(
		kgo.SeedBrokers(conn.Address),
		kgo.DialTLSConfig(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}),
		kgo.SASL(scram.Auth{User: conn.User, Pass: conn.Password}.AsSha256Mechanism()),
	)
	if err != nil {
		return nil, err
	}
	return &Admin{Client: kadm.NewClient(kafka), kafka: kafka}, nil
}

// Publish writes one record and waits until the broker has stored it.
func (admin *Admin) Publish(ctx context.Context, topic string, key string, value []byte) error {
	record := &kgo.Record{Topic: topic, Key: []byte(key), Value: value}
	return admin.kafka.ProduceSync(ctx, record).FirstErr()
}
