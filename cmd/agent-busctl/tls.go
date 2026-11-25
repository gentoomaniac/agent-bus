package main

import (
	"fmt"
	"path/filepath"
	"time"

	"git.srv.gentoomaniac.net/marco/agent-messenger/internal/tlsinit"
)

type TLSInitCmd struct {
	CAKey      string        `help:"CA private key. Default: ca.key in the config directory." type:"path"`
	BrokerCert string        `help:"Broker certificate. Default: broker.pem in the config directory." type:"path"`
	BrokerKey  string        `help:"Broker private key. Default: broker.key in the config directory." type:"path"`
	SAN        []string      `help:"DNS name or IP the broker certificate is valid for; repeatable. Default: localhost, 127.0.0.1, ::1 and this machine's hostname."`
	Validity   time.Duration `help:"How long the broker certificate is valid." default:"17520h"`
	Force      bool          `help:"Overwrite existing files. Clients must then be given the new CA certificate."`
}

func (cmd *TLSInitCmd) Run(cli *CLI) error {
	sans := cmd.SAN
	if len(sans) == 0 {
		sans = tlsinit.DefaultSANs()
	}
	opts := tlsinit.Options{
		CACertPath:     cli.caCertPath(),
		CAKeyPath:      orDefault(cmd.CAKey, cli.ConfigDir, "ca.key"),
		BrokerCertPath: orDefault(cmd.BrokerCert, cli.ConfigDir, "broker.pem"),
		BrokerKeyPath:  orDefault(cmd.BrokerKey, cli.ConfigDir, "broker.key"),
		SANs:           sans,
		BrokerValidity: cmd.Validity,
		Overwrite:      cmd.Force,
	}
	if err := tlsinit.Create(opts); err != nil {
		return err
	}
	fmt.Printf("CA certificate:     %s\nCA key:             %s\nBroker certificate: %s\nBroker key:         %s\nBroker valid for:   %v\n",
		opts.CACertPath, opts.CAKeyPath, opts.BrokerCertPath, opts.BrokerKeyPath, sans)
	return nil
}

// orDefault returns the flag value, or name inside the config directory when the flag is empty.
func orDefault(flagValue, configDir, name string) string {
	if flagValue != "" {
		return flagValue
	}
	return filepath.Join(configDir, name)
}

type TLSBrokerCmd struct {
	BrokerCert string        `help:"Broker certificate. Default: broker.pem in the config directory." type:"path"`
	BrokerKey  string        `help:"Broker private key. Default: broker.key in the config directory." type:"path"`
	CAKey      string        `help:"CA private key. Default: ca.key in the config directory." type:"path"`
	SAN        []string      `help:"DNS name or IP the broker certificate is valid for; repeatable. Name every one clients use: the list replaces the old one." required:""`
	Validity   time.Duration `help:"How long the broker certificate is valid." default:"17520h"`
}

// Run replaces the broker's certificate, signed by the existing CA: clients keep their ca.pem and
// their certificates. The broker uses it from its next start.
func (cmd *TLSBrokerCmd) Run(cli *CLI) error {
	opts := tlsinit.Options{
		CACertPath:     cli.caCertPath(),
		CAKeyPath:      orDefault(cmd.CAKey, cli.ConfigDir, "ca.key"),
		BrokerCertPath: orDefault(cmd.BrokerCert, cli.ConfigDir, "broker.pem"),
		BrokerKeyPath:  orDefault(cmd.BrokerKey, cli.ConfigDir, "broker.key"),
		SANs:           cmd.SAN,
		BrokerValidity: cmd.Validity,
	}
	if err := tlsinit.RenewBroker(opts); err != nil {
		return err
	}
	fmt.Printf("Broker certificate: %s\nBroker key:         %s\nBroker valid for:   %v\nSame CA: clients need nothing new. Restart the broker to use it.\n",
		opts.BrokerCertPath, opts.BrokerKeyPath, cmd.SAN)
	return nil
}
