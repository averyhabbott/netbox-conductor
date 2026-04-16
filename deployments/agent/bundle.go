// Package agentbundle embeds the static agent support files so the conductor
// can bundle them into downloadable tarballs without needing them on disk.
package agentbundle

import _ "embed"

// EnvExample is the contents of netbox-agent.env.example.
//
//go:embed netbox-agent.env.example
var EnvExample []byte

// ServiceFile is the contents of netbox-agent.service.
//
//go:embed netbox-agent.service
var ServiceFile []byte

// InstallScript is the contents of install.sh.
//
//go:embed install.sh
var InstallScript []byte

// SudoersFile is the contents of netbox-agent-sudoers, installed to
// /etc/sudoers.d/netbox-agent during agent setup.
//
//go:embed netbox-agent-sudoers
var SudoersFile []byte

// NginxConf is the nginx site config for NetBox + conductor health checks.
//
//go:embed nginx-netbox-conductor.conf
var NginxConf []byte

// ApacheConf is the Apache site config for NetBox + conductor health checks.
//
//go:embed apache-netbox-conductor.conf
var ApacheConf []byte
