package broker

import (
	"fmt"
	"net"
	"strconv"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/creds"
)

// DefaultMappedPort is the port of the listener for certificate logins (BUS_MAPPED_PORT).
const DefaultMappedPort = 4223

// MappedAddress is where the certificate listener is, given the address of the password listener:
// the same host, the mapped port, over WebSocket (nats-server has one client port per server, and
// the WebSocket listener is the second TLS listener).
func MappedAddress(brokerAddress string, mappedPort int) (string, error) {
	host, _, err := net.SplitHostPort(brokerAddress)
	if err != nil {
		return "", fmt.Errorf("broker address %q: %w", brokerAddress, err)
	}
	return "wss://" + net.JoinHostPort(host, strconv.Itoa(mappedPort)), nil
}

// UserConnection is how to connect as the user whose credentials are in dir: with the client
// certificate if dir has one, else with its password. user is the password login's name.
func UserConnection(dir, user, brokerAddress string, mappedPort int, caCertPath string) (Connection, error) {
	if creds.HasClientCert(dir) {
		address, err := MappedAddress(brokerAddress, mappedPort)
		if err != nil {
			return Connection{}, err
		}
		certPath, keyPath := creds.ClientCertPaths(dir)
		return Connection{Address: address, CACertPath: caCertPath, User: user, ClientCertPath: certPath, ClientKeyPath: keyPath}, nil
	}
	password, err := creds.ReadPassword(dir)
	if err != nil {
		return Connection{}, err
	}
	return Connection{Address: brokerAddress, CACertPath: caCertPath, User: user, Password: password}, nil
}
