// The network fence provides Garden's networking function.
package network

import (
	"flag"
	"net"

	"github.com/cloudfoundry-incubator/garden-linux/fences"
	"github.com/cloudfoundry-incubator/garden-linux/fences/network/subnets"
	"github.com/cloudfoundry/gunk/localip"
)

const (
	DefaultNetworkPool        = "10.254.0.0/22"
	DefaultMTUSize     MtuVar = 1500
)

type Config struct {
	Network    CidrVar
	Mtu        MtuVar
	ExternalIP IPVar
	BinPath    string
}

func init() {
	config := &Config{}
	fences.Register(config.Init, config.Main)
}

func (config *Config) Init(fs *flag.FlagSet) error {
	localIP, err := localip.LocalIP()
	if err != nil {
		return err
	}

	config.Network = cidrVar(DefaultNetworkPool)
	config.Mtu = DefaultMTUSize
	config.ExternalIP = IPVar{net.ParseIP(localIP)}

	fs.Var(&config.Network, "networkPool",
		"Pool of dynamically allocated container subnets")

	fs.Var(&config.Mtu, "mtu",
		"MTU size for container network interfaces")

	fs.Var(&config.ExternalIP, "externalIP",
		"IP address to use to reach container's mapped ports")

	fs.StringVar(&config.BinPath, "binPath", "./", "path to hold binaries")

	return nil
}

func (config *Config) Main(registry *fences.BuilderRegistry) error {
	subnets, err := subnets.New(config.Network.IPNet)
	if err != nil {
		return err
	}

	fenceBldr := &f{subnets, uint32(config.Mtu), config.ExternalIP.IP, config.BinPath}
	registry.Register(fenceBldr)

	return nil
}
