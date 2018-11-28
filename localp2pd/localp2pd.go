package pluginlocalp2pd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/ipfs/iptb/testbed/interfaces"
	client "github.com/libp2p/go-libp2p-daemon/p2pclient"
	peer "github.com/libp2p/go-libp2p-peer"
	ma "github.com/multiformats/go-multiaddr"
)

// PluginName is the libp2p daemon IPTB plugin name.
var PluginName = "localp2pd"

// type check
var _ testbedi.Core = (*LocalP2pd)(nil)

type connManagerConfig struct {
	lowWatermark  *int
	highWatermark *int
	gracePeriod   *int
}

// LocalP2pd wraps behaviors of the libp2p daemon.
type LocalP2pd struct {
	// config options
	command        string
	dir            string
	connManager    *connManagerConfig
	dhtMode        string
	bootstrap      bool
	bootstrapPeers string

	// process
	process *os.Process
	alive   bool

	// client
	mclient sync.Mutex
	client  *client.Client
}

// NewNode creates a localp2pd iptb core node that runs the libp2p daemon on the
// local system using control sockets within the specified directory.
// Attributes:
// - controladdr:
// func NewNode(dir string, attrs map[string]string) (testbedi.Core, error) {
func NewNode(dir string, attrs map[string]string) (*LocalP2pd, error) {
	// defaults
	var (
		connManager    *connManagerConfig
		dhtMode        string
		found          bool
		err            error
		bootstrapPeers string
		command        string
		bootstrap      = false
	)

	if dhtMode, found = attrs["dhtmode"]; !found {
		dhtMode = "off"
	}

	if _, found = attrs["connmanager"]; found {
		connManager = &connManagerConfig{}
	}

	if lowmark, ok := attrs["connmanagerlowmark"]; ok {
		if connManager == nil {
			return nil, errors.New("conn manager low watermark provided without enabling conn manager")
		}
		var lowmarki int
		if lowmarki, err = strconv.Atoi(lowmark); err != nil {
			return nil, fmt.Errorf("parsing low watermark: %s", err)
		}
		connManager.lowWatermark = &lowmarki
	}

	if highmark, ok := attrs["connmanagerhighmark"]; ok {
		if connManager == nil {
			return nil, errors.New("conn manager high watermark provided without enabling conn manager")
		}
		var highmarki int
		if highmarki, err = strconv.Atoi(highmark); err != nil {
			return nil, fmt.Errorf("parsing low watermark: %s", err)
		}
		connManager.highWatermark = &highmarki
	}

	if graceperiod, ok := attrs["connmanagergraceperiod"]; ok {
		if connManager == nil {
			return nil, errors.New("conn manager grace period provided without enabling conn manager")
		}
		var graceperiodi int
		if graceperiodi, err = strconv.Atoi(graceperiod); err != nil {
			return nil, fmt.Errorf("parsing low watermark: %s", err)
		}
		connManager.gracePeriod = &graceperiodi
	}

	if _, found = attrs["bootstrap"]; found {
		bootstrap = true
	}

	if bootstrapPeers, found = attrs["bootstrapPeers"]; !found {
		bootstrapPeers = ""
	}

	if command, found = attrs["command"]; !found {
		command = ""
	}

	p2pd := &LocalP2pd{
		command:        command,
		dir:            dir,
		dhtMode:        dhtMode,
		connManager:    connManager,
		bootstrap:      bootstrap,
		bootstrapPeers: bootstrapPeers,
		alive:          false,
	}
	return p2pd, nil
}

func (l *LocalP2pd) sockPath() string {
	return l.dir + "/p2pd.sock"
}

func (l *LocalP2pd) cmdArgs() []string {
	var args []string

	switch l.dhtMode {
	case "full":
		args = append(args, "-dht")
	case "client":
		args = append(args, "-dhtClient")
	}

	if l.bootstrap {
		args = append(args, "-b")
	}

	if l.bootstrapPeers != "" {
		args = append(args, "-bootstrapPeers", l.bootstrapPeers)
	}

	if l.connManager != nil {
		args = append(args, "-connManager")

		if l.connManager.gracePeriod != nil {
			args = append(args, "-connGrace", strconv.Itoa(*l.connManager.gracePeriod))
		}

		if l.connManager.highWatermark != nil {
			args = append(args, "-connHi", strconv.Itoa(*l.connManager.highWatermark))
		}

		if l.connManager.lowWatermark != nil {
			args = append(args, "-connLo", strconv.Itoa(*l.connManager.lowWatermark))
		}

		args = append(args, "-sock", l.sockPath())
	}

	return args
}

// PeerID returns the peer id
func (l *LocalP2pd) PeerID() (string, error) {
	client, releaseClient, err := l.acquireClient()
	defer releaseClient()
	if err != nil {
		return "", err
	}
	peerID, _, err := client.Identify()
	if err != nil {
		return "", err
	}
	return peerID.String(), nil
}

// APIAddr returns the multiaddr for the api
func (l *LocalP2pd) APIAddr() (string, error) {
	return l.sockPath(), nil
}

// SwarmAddrs returns the swarm addrs for the node
func (l *LocalP2pd) SwarmAddrs() ([]string, error) {
	client, releaseClient, err := l.acquireClient()
	defer releaseClient()
	if err != nil {
		return nil, err
	}
	_, addrs, err := client.Identify()
	if err != nil {
		return nil, err
	}
	addrstrs := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addrstrs = append(addrstrs, addr.String())
	}
	return addrstrs, nil
}

// Init is a no-op
func (l *LocalP2pd) Init(ctx context.Context, args ...string) (testbedi.Output, error) {
	return nil, nil
}

// Start launches a libp2p daemon
func (l *LocalP2pd) Start(ctx context.Context, wait bool, args ...string) (testbedi.Output, error) {
	if l.alive {
		return nil, fmt.Errorf("libp2p daemon is already running")
	}

	// set up command
	cmdargs := append(l.cmdArgs(), args...)
	cmd := exec.Command(l.command, cmdargs...)
	cmd.Dir = l.dir

	stdout, err := os.Create(filepath.Join(l.dir, "p2pd.stdout"))
	if err != nil {
		return nil, err
	}

	stderr, err := os.Create(filepath.Join(l.dir, "p2pd.stderr"))
	if err != nil {
		return nil, err
	}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	l.process = cmd.Process
	pid := l.process.Pid

	err = ioutil.WriteFile(filepath.Join(l.dir, "p2pd.pid"), []byte(fmt.Sprint(pid)), 0666)
	if err != nil {
		return nil, fmt.Errorf("writing libp2p daemon pid: %s", err)
	}

	if wait {
		for i := 0; i < 50; i++ {
			_, err := os.Stat(l.sockPath())
			if err != nil {
				time.Sleep(time.Millisecond * 400)
				continue
			}
			return nil, nil
		}
		return nil, fmt.Errorf("libp2p daemon with pid %d failed to come online ", pid)
	}

	return nil, nil
}

// Stop shuts down the daemon
func (l *LocalP2pd) Stop(ctx context.Context) error {
	// Stop a client if it exists
	l.mclient.Lock()
	if l.client != nil {
		if err := l.client.Close(); err != nil {
			return err
		}
		l.client = nil
	}
	l.mclient.Unlock()

	proc := l.process

	waitch := make(chan struct{}, 1)
	go func() {
		proc.Wait()
		waitch <- struct{}{}
	}()

	// cleanup
	defer func() {
		os.Remove(filepath.Join(l.dir, "p2pd.pid"))
	}()

	for i := 0; i < 2; i++ {
		if err := l.signalAndWait(waitch, syscall.SIGTERM, time.Second*5); err != errTimeout {
			return err
		}
	}

	if err := l.signalAndWait(waitch, syscall.SIGQUIT, time.Second*5); err != errTimeout {
		return err
	}

	if err := l.signalAndWait(waitch, syscall.SIGKILL, time.Second*5); err != errTimeout {
		return err
	}

	return nil
}

var errTimeout = fmt.Errorf("timed out waiting for process to exit")

func (l *LocalP2pd) signalAndWait(waitch chan struct{}, sig syscall.Signal, timeout time.Duration) error {
	if err := l.process.Signal(sig); err != nil {
		return err
	}

	select {
	case <-waitch:
		return nil
	case <-time.After(timeout):
		return errTimeout
	}
}

// RunCmd is a no-op for the libp2p daemon
func (l *LocalP2pd) RunCmd(ctx context.Context, stdin io.Reader, args ...string) (testbedi.Output, error) {
	return nil, nil
}

// Connect the node to another
func (l *LocalP2pd) Connect(ctx context.Context, n testbedi.Core) error {
	client, releaseClient, err := l.acquireClient()
	defer releaseClient()
	if err != nil {
		return err
	}

	peerstr, err := n.PeerID()
	if err != nil {
		return err
	}
	peer := peer.ID(peerstr)

	var addrs []ma.Multiaddr
	addrstrs, err := n.SwarmAddrs()
	if err != nil {
		return err
	}
	for _, addrstr := range addrstrs {
		addr, err := ma.NewMultiaddr(addrstr)
		// log?
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}

	return client.Connect(peer, addrs)
}

func (l *LocalP2pd) acquireClient() (*client.Client, func(), error) {
	l.mclient.Lock()
	if l.client == nil {
		client, err := client.NewClient(l.sockPath(), filepath.Join(l.dir, "p2pclient.sock"))
		if err != nil {
			return nil, nil, err
		}
		l.client = client
	}
	return l.client, l.mclient.Unlock, nil
}

// Shell is a no-op for the libp2p daemon.
func (l *LocalP2pd) Shell(ctx context.Context, ns []testbedi.Core) error {
	return nil
}

// Dir returns the iptb directory assigned to the node
func (l *LocalP2pd) Dir() string {
	return l.dir
}

// Type returns a string that identifies the implementation
// Examples localipfs, dockeripfs, etc.
func (l *LocalP2pd) Type() string {
	return "localp2pd"
}

func (l *LocalP2pd) String() string {
	pcid, err := l.PeerID()
	if err != nil {
		return fmt.Sprintf("%s", l.Type())
	}
	return fmt.Sprintf("%s{%s}", l.Type(), pcid[0:12])
}
