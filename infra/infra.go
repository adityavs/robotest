package infra

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"

	log "github.com/Sirupsen/logrus"
	"github.com/gravitational/robotest/lib/ssh"
	"github.com/gravitational/trace"
)

func New(conf Config) (Infra, error) {
	return &autoCluster{
		nodes:        conf.InitialCluster,
		opsCenterURL: conf.OpsCenterURL,
	}, nil
}

func NewWizard(conf Config, provisioner Provisioner) (Infra, *ProvisionerOutput, error) {
	cluster, err := startWizard(provisioner)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	return cluster, &cluster.ProvisionerOutput, nil
}

type Provisioner interface {
	Create() (*ProvisionerOutput, error)
	Destroy() error
	Connect(addr string) (*ssh.Session, error)
	// SelectInterface returns the index (in addrs) of network address to use for
	// installation.
	// addrs is guaranteed to have at least one element
	SelectInterface(output ProvisionerOutput, addrs []string) (int, error)
	StartInstall(session *ssh.Session) error
	Nodes() []Node
	NumNodes() int
	// Allocate allocates a new node (from the pool of available nodes)
	// and returns a reference to it
	Allocate() (Node, error)
	// Deallocate places specified node back to the node pool
	Deallocate(Node) error
}

type Infra interface {
	OpsCenterURL() string
	// Close closes the cluster resources
	Close() error
	// Run runs the specified command on all active nodes in the cluster
	// Run(command string) error
	// Provisioner returns the provisioner used to manage nodes in the cluster
	Provisioner() Provisioner
}

type Node interface {
	Connect() (*ssh.Session, error)
}

type ProvisionerOutput struct {
	InstallerIP  string
	PrivateIPs   []string
	PublicIPs    []string
	InstallerURL url.URL
}

func (r ProvisionerOutput) String() string {
	return fmt.Sprintf("ProvisionerOutput(installer IP=%v, private IPs=%v, public IPs=%v)",
		r.InstallerIP, r.PrivateIPs, r.PublicIPs)
}

func Distribute(command string, nodes []Node) error {
	log.Infof("running %q on %v", command, nodes)
	errCh := make(chan error, len(nodes))
	wg := sync.WaitGroup{}
	wg.Add(len(nodes))
	for _, node := range nodes {
		go func(errCh chan<- error) {
			log.Infof("running on %v", node)
			errCh <- Run(node, command, os.Stderr)
			wg.Done()
		}(errCh)
	}
	wg.Wait()
	close(errCh)
	var errors []error
	for err := range errCh {
		if err != nil {
			errors = append(errors, err)
		}
	}
	return trace.NewAggregate(errors...)
}

func Run(node Node, command string, w io.Writer) error {
	session, err := node.Connect()
	if err != nil {
		return trace.Wrap(err)
	}
	return sshutils.RunCommandWithOutput(session, command, w)
}
