package main

import (
	"bytes"
	"errors"
	"flag"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/docker/docker/daemon/graphdriver"
	_ "github.com/docker/docker/daemon/graphdriver/aufs"
	_ "github.com/docker/docker/daemon/graphdriver/vfs"
	"github.com/docker/docker/graph"
	"github.com/docker/docker/registry"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/garden/server"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/container_pool"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/container_pool/repository_fetcher"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/container_pool/rootfs_provider"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/network_pool"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/port_pool"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/quota_manager"
	"github.com/cloudfoundry-incubator/warden-linux/linux_backend/uid_pool"
	"github.com/cloudfoundry-incubator/warden-linux/sysconfig"
	"github.com/cloudfoundry-incubator/warden-linux/system_info"
	"github.com/cloudfoundry/gunk/command_runner/linux_command_runner"
)

var listenNetwork = flag.String(
	"listenNetwork",
	"unix",
	"how to listen on the address (unix, tcp, etc.)",
)

var listenAddr = flag.String(
	"listenAddr",
	"/tmp/warden.sock",
	"address to listen on",
)

var snapshotsPath = flag.String(
	"snapshots",
	"",
	"directory in which to store container state to persist through restarts",
)

var binPath = flag.String(
	"bin",
	"",
	"directory containing backend-specific scripts (i.e. ./create.sh)",
)

var depotPath = flag.String(
	"depot",
	"",
	"directory in which to store containers",
)

var overlaysPath = flag.String(
	"overlays",
	"",
	"directory in which to store containers mount points",
)

var rootFSPath = flag.String(
	"rootfs",
	"",
	"directory of the rootfs for the containers",
)

var disableQuotas = flag.Bool(
	"disableQuotas",
	false,
	"disable disk quotas",
)

var containerGraceTime = flag.Duration(
	"containerGraceTime",
	0,
	"time after which to destroy idle containers",
)

var networkPool = flag.String(
	"networkPool",
	"10.254.0.0/22",
	"network pool CIDR for containers; each container will get a /30",
)

var portPoolStart = flag.Uint(
	"portPoolStart",
	61001,
	"start of ephemeral port range used for mapped container ports",
)

var portPoolSize = flag.Uint(
	"portPoolSize",
	5000,
	"size of port pool used for mapped container ports",
)

var uidPoolStart = flag.Uint(
	"uidPoolStart",
	10000,
	"start of per-container user ids",
)

var uidPoolSize = flag.Uint(
	"uidPoolSize",
	256,
	"size of the uid pool",
)

var denyNetworks = flag.String(
	"denyNetworks",
	"",
	"CIDR blocks representing IPs to blacklist",
)

var allowNetworks = flag.String(
	"allowNetworks",
	"",
	"CIDR blocks representing IPs to whitelist",
)

var graphRoot = flag.String(
	"graph",
	"/var/lib/warden-docker-graph",
	"docker image graph",
)

var dockerRegistry = flag.String(
	"registry",
	registry.IndexServerAddress(),
	"docker registry API endpoint",
)

var tag = flag.String(
	"tag",
	"",
	"server-wide identifier used for 'global' configuration",
)

func main() {
	flag.Parse()

	cf_debug_server.Run()

	runtime.GOMAXPROCS(runtime.NumCPU())

	logger := cf_lager.New("warden-linux")

	if *binPath == "" {
		missing("-bin")
	}

	if *depotPath == "" {
		missing("-depot")
	}

	if *overlaysPath == "" {
		missing("-overlays")
	}

	uidPool := uid_pool.New(uint32(*uidPoolStart), uint32(*uidPoolSize))

	_, ipNet, err := net.ParseCIDR(*networkPool)
	if err != nil {
		logger.Fatal("malformed-network-pool", err)
	}

	networkPool := network_pool.New(ipNet)

	// TODO: use /proc/sys/net/ipv4/ip_local_port_range by default (end + 1)
	portPool := port_pool.New(uint32(*portPoolStart), uint32(*portPoolSize))

	config := sysconfig.NewConfig(*tag)

	runner := sysconfig.NewRunner(config, linux_command_runner.New())

	quotaManager := quota_manager.New(runner, getMountPoint(logger, *depotPath), *binPath)

	if *disableQuotas {
		quotaManager.Disable()
	}

	if err := os.MkdirAll(*graphRoot, 0755); err != nil {
		logger.Fatal("failed-to-create-graph-directory", err)
	}

	graphRootStat := syscall.Stat_t{}
	if err := syscall.Stat(*graphRoot, &graphRootStat); err != nil {
		logger.Fatal("failed-to-stat-graph-directory", err)
	}

	rootFSStat := syscall.Stat_t{}
	if err := syscall.Stat(*rootFSPath, &rootFSStat); err != nil {
		logger.Fatal("failed-to-stat-rootfs-directory", err)
	}

	if graphRootStat.Dev == rootFSStat.Dev {
		logger.Fatal("graphfs-and-rootfs-device-conflict",
			errors.New("-graph and -rootfs must be on different devices"))
	}

	graphDriver, err := graphdriver.New(*graphRoot, nil)
	if err != nil {
		logger.Fatal("failed-to-construct-graph-driver", err)
	}

	graph, err := graph.NewGraph(*graphRoot, graphDriver)
	if err != nil {
		logger.Fatal("failed-to-construct-graph", err)
	}

	reg, err := registry.NewSession(nil, nil, *dockerRegistry, true)
	if err != nil {
		logger.Fatal("failed-to-construct-registry", err)
	}

	repoFetcher := repository_fetcher.Retryable{repository_fetcher.New(reg, graph)}
	rootFSProviders := map[string]rootfs_provider.RootFSProvider{
		"":       rootfs_provider.NewOverlay(*binPath, *overlaysPath, *rootFSPath, runner),
		"docker": rootfs_provider.NewDocker(repoFetcher, graphDriver),
	}

	pool := container_pool.New(
		logger,
		*binPath,
		*depotPath,
		config,
		rootFSProviders,
		uidPool,
		networkPool,
		portPool,
		strings.Split(*denyNetworks, ","),
		strings.Split(*allowNetworks, ","),
		runner,
		quotaManager,
	)

	systemInfo := system_info.NewProvider(*depotPath)

	backend := linux_backend.New(logger, pool, systemInfo, *snapshotsPath)

	err = backend.Setup()
	if err != nil {
		logger.Fatal("failed-to-set-up-backend", err)
	}

	graceTime := *containerGraceTime

	wardenServer := server.New(*listenNetwork, *listenAddr, graceTime, backend, logger)

	err = wardenServer.Start()
	if err != nil {
		logger.Fatal("failed-to-start-server", err)
	}

	logger.Info("started", lager.Data{
		"network": *listenNetwork,
		"addr":    *listenAddr,
	})

	signals := make(chan os.Signal, 1)

	go func() {
		<-signals
		wardenServer.Stop()
		os.Exit(0)
	}()

	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	select {}
}

func getMountPoint(logger lager.Logger, depotPath string) string {
	dfOut := new(bytes.Buffer)

	df := exec.Command("df", depotPath)
	df.Stdout = dfOut
	df.Stderr = os.Stderr

	err := df.Run()
	if err != nil {
		logger.Fatal("failed-to-get-mount-info", err)
	}

	dfOutputWords := strings.Split(string(dfOut.Bytes()), " ")

	return strings.Trim(dfOutputWords[len(dfOutputWords)-1], "\n")
}

func missing(flagName string) {
	println("missing " + flagName)
	println()
	flag.Usage()
}
