/*
Copyright 2018 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/gravitational/robotest/infra"
	"github.com/gravitational/robotest/lib/constants"
	"github.com/gravitational/robotest/lib/defaults"
	sshutils "github.com/gravitational/robotest/lib/ssh"
	"github.com/gravitational/robotest/lib/wait"

	"github.com/cenkalti/backoff"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

// Gravity is interface to remote gravity CLI
type Gravity interface {
	json.Marshaler
	fmt.Stringer
	// SetInstaller transfers and prepares installer package given with installerUrl.
	// The install directory will be overridden to the specified sub-directory
	// in user's home
	SetInstaller(ctx context.Context, installerUrl, subdir string) error
	// TransferFile transfers the file specified with url into the given sub-directory
	// subdir in user's home.
	// The install directory will be overridden to the specified sub-directory
	// in user's home
	TransferFile(ctx context.Context, url, subdir string) error
	// ExecScript transfers and executes script with predefined parameters
	ExecScript(ctx context.Context, scriptUrl string, args []string) error
	// Install operates on initial master node
	Install(ctx context.Context, param InstallParam) error
	// Status retrieves status
	Status(ctx context.Context) (*GravityStatus, error)
	// OfflineUpdate tries to upgrade application version
	OfflineUpdate(ctx context.Context, installerUrl string) error
	// Join asks to join existing cluster (or installation in progress)
	Join(ctx context.Context, param JoinCmd) error
	// Leave requests current node leave a cluster
	Leave(ctx context.Context, graceful Graceful) error
	// Remove requests cluster to evict a given node
	Remove(ctx context.Context, node string, graceful Graceful) error
	// Uninstall will wipe gravity installation from node
	Uninstall(ctx context.Context) error
	// UninstallApp uninstalls cluster application
	UninstallApp(ctx context.Context) error
	// PowerOff will power off the node
	PowerOff(ctx context.Context, graceful Graceful) error
	// Reboot will reboot this node and wait until it will become available again
	Reboot(ctx context.Context, graceful Graceful) error
	// CollectLogs will pull essential logs from node and store it in state dir under node-logs/prefix
	CollectLogs(ctx context.Context, prefix string, args ...string) (localPath string, err error)
	// Upload uploads packages in current installer dir to cluster
	Upload(ctx context.Context) error
	// Upgrade takes currently active installer (see SetInstaller) and tries to perform upgrade
	Upgrade(ctx context.Context) error
	// RunInPlanet runs specific command inside Planet container and returns its result
	RunInPlanet(ctx context.Context, cmd string, args ...string) (string, error)
	// Node returns underlying VM instance
	Node() infra.Node
	// Offline returns true if node was previously powered off
	Offline() bool
	// Client returns SSH client to VM instance
	Client() *ssh.Client
	// Will log using extended info such as current tag, node info, etc
	Logger() logrus.FieldLogger
	// IsLeader returns true if node is leader
	IsLeader(ctx context.Context) bool
	// PartitionNetwork creates a network partition between this gravity node and
	// the cluster.
	PartitionNetwork(ctx context.Context, nodes []Gravity) error
	// UnpartitionNetwork removes network partition between this gravity node and
	// the cluster.
	UnpartitionNetwork(ctx context.Context, nodes []Gravity) error
}

type Graceful bool

// InstallParam represents install parameters passed to first node
type InstallParam struct {
	// Token is initial token to use during cluster setup
	Token string `json:"-"`
	// Role is node role as defined in app.yaml
	Role string `json:"role" validate:"required"`
	// Cluster is Optional name of the cluster. Autogenerated if not set.
	Cluster string `json:"cluster"`
	// Flavor is Application flavor. See Application Manifest for details.
	Flavor string `json:"flavor" validate:"required"`
	// K8SConfigURL is (Optional) File with Kubernetes resources to create in the cluster during installation.
	K8SConfigURL string `json:"k8s_config_url,omitempty"`
	// PodNetworkCidr is (Optional) CIDR range Kubernetes will be allocating node subnets and pod IPs from. Must be a minimum of /16 so Kubernetes is able to allocate /24 to each node. Defaults to 10.244.0.0/16.
	PodNetworkCIDR string `json:"pod_network_cidr,omitempty"`
	// ServiceCidr (Optional) CIDR range Kubernetes will be allocating service IPs from. Defaults to 10.100.0.0/16.
	ServiceCIDR string `json:"service_cidr,omitempty"`
	// EnableRemoteSupport (Optional) whether to register this installation with remote ops-center
	EnableRemoteSupport bool `json:"remote_support"`
	// LicenseURL (Optional) is license file, could be local or s3 or http(s) url
	LicenseURL string `json:"license,omitempty"`
	// CloudProvider defines tighter integration with cloud vendor, i.e. use AWS networking on Amazon
	CloudProvider string `json:"cloud_provider,omitempty"`
	// GCENodeTag specifies the node tag on GCE.
	// Node tag replaces the cluster name if the cluster name does not comply with the GCE naming convention
	GCENodeTag string `json:"gce_node_tag,omitempty"`
	// StateDir is the directory where all gravity data will be stored on the node
	StateDir string `json:"state_dir" validate:"required"`
	// OSFlavor is operating system and optional version separated by ':'
	OSFlavor OS `json:"os" validate:"required"`
	// DockerStorageDriver is one of supported storage drivers
	DockerStorageDriver StorageDriver `json:"storage_driver"`
	// InstallerURL overrides installer URL from the global config
	InstallerURL string `json:"installer_url,omitempty"`
	// OpsAdvertiseAddr is optional Ops Center advertise address to pass to the install command
	OpsAdvertiseAddr string `json:"ops_advertise_addr,omitempty"`
}

// JoinCmd represents various parameters for Join
type JoinCmd struct {
	// InstallDir is set automatically
	InstallDir string
	// PeerAddr is other node (i.e. master)
	PeerAddr string
	// Token is the join token
	Token string
	// Role is the role of the joining node
	Role string
	// StateDir is where all gravity data will be stored on the joining node
	StateDir string
}

// GravityStatus describes the status of the Gravity cluster
type GravityStatus struct {
	// Cluster describes the cluster status
	Cluster ClusterStatus `json:"cluster"`
}

// ClusterStatus describes the status of a Gravity cluster
type ClusterStatus struct {
	// Application defines the cluster application
	Application Application `json:"application"`
	// Cluster is the name of the cluster
	Cluster string `json:"domain"`
	// Status is the cluster status
	Status string `json:"state"`
	// Token is secure token which prevents rogue nodes from joining the cluster during installation
	Token Token `json:"token"`
	// Nodes describes the nodes in the cluster
	Nodes []NodeStatus `json:"nodes"`
}

const (
	// StatusActive indicates that the Gravity cluster is in a healthy state.
	StatusActive = "active"
	// StatusDegraded indicates that the Gravity cluster is in a degraded state
	// but still functional.
	StatusDegraded = "degraded"
	// TODO: add additional state
)

// Application defines the cluster application
type Application struct {
	// Name is the name of the cluster application
	Name string `json:"name"`
}

// NodeStatus describes the status of a cluster node
type NodeStatus struct {
	// Addr is the advertised address of this cluster node
	Addr string `json:"advertise_ip"`
}

// Token describes the cluster join token
type Token struct {
	// Token is the join token value
	Token string `json:"token"`
}

type gravity struct {
	node       infra.Node
	ssh        *ssh.Client
	installDir string
	param      cloudDynamicParams
	ts         time.Time
	log        logrus.FieldLogger
}

func (g *gravity) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"public_ip": g.node.Addr(),
		"ip":        g.node.PrivateAddr(),
	})
}

// waits for SSH to be up on node and returns client
func sshClient(ctx context.Context, node infra.Node, log logrus.FieldLogger) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, deadlineSSH)
	defer cancel()

	var client *ssh.Client
	b := backoff.NewConstantBackOff(retrySSH)
	err := wait.RetryWithInterval(ctx, b, func() (err error) {
		client, err = node.Client()
		if err == nil {
			log.Debug("Connected via SSH.")
			return nil
		}

		log.WithError(err).Debug("Waiting for SSH.")
		return trace.Wrap(err)
	}, log)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return client, nil
}

func (g *gravity) Logger() logrus.FieldLogger {
	return g.log
}

// String returns public and private addresses of the node
func (g *gravity) String() string {
	return fmt.Sprintf("node(private_addr=%s, public_addr=%s)",
		g.node.PrivateAddr(), g.node.Addr())
}

func (g *gravity) Node() infra.Node {
	return g.node
}

// Client returns SSH client to the node
func (g *gravity) Client() *ssh.Client {
	return g.ssh
}

// Install runs gravity install with params
func (g *gravity) Install(ctx context.Context, param InstallParam) error {
	// cmd specify additional configuration for the install command
	// collected from defaults and/or computed values
	type cmd struct {
		InstallDir    string
		PrivateAddr   string
		DockerDevice  string
		StorageDriver string
		InstallParam
	}

	dockerDevice := g.param.dockerDevice
	if g.param.storageDriver != constants.DeviceMapper {
		// Docker device is not used with non-devicemapper storage drivers
		dockerDevice = ""
	}

	config := cmd{
		InstallDir:    g.installDir,
		PrivateAddr:   g.Node().PrivateAddr(),
		DockerDevice:  dockerDevice,
		StorageDriver: g.param.storageDriver.Driver(),
		InstallParam:  param,
	}

	var buf bytes.Buffer
	err := installCmdTemplate.Execute(&buf, config)
	if err != nil {
		return trace.Wrap(err, buf.String())
	}

	err = sshutils.Run(ctx, g.Client(), g.Logger(), buf.String(), nil)
	return trace.Wrap(err, param)
}

var installCmdTemplate = template.Must(
	template.New("gravity_install").Parse(`
		cd {{.InstallDir}} && ./gravity version && sudo ./gravity install --debug \
		--advertise-addr={{.PrivateAddr}} --token={{.Token}} --flavor={{.Flavor}} \
		--docker-device={{.DockerDevice}} \
		{{if .StorageDriver}}--storage-driver={{.StorageDriver}}{{end}} \
		--system-log-file=./telekube-system.log \
		--cloud-provider=generic --state-dir={{.StateDir}} \
		--httpprofile=localhost:6061 \
		{{if .Cluster}}--cluster={{.Cluster}}{{end}} \
		{{if .OpsAdvertiseAddr}}--ops-advertise-addr={{.OpsAdvertiseAddr}}{{end}}
`))

// Status queries cluster status
func (g *gravity) Status(ctx context.Context) (*GravityStatus, error) {
	cmd := "sudo gravity status --output=json --system-log-file=./telekube-system.log"
	status := GravityStatus{}
	err := sshutils.RunAndParse(ctx, g.Client(), g.Logger(), cmd, nil, parseStatus(&status))
	if err != nil {
		if exitErr, ok := trace.Unwrap(err).(sshutils.ExitStatusError); ok {
			g.Logger().WithFields(logrus.Fields{
				"private_addr": g.Node().PrivateAddr(),
				"addr":         g.Node().Addr(),
				"command":      cmd,
				"exit code":    exitErr.ExitStatus(),
			}).Warn("Failed.")
		}
		return nil, trace.Wrap(err, cmd)

	}

	return &status, nil
}

func (g *gravity) OfflineUpdate(ctx context.Context, installerUrl string) error {
	return nil
}

func (g *gravity) Join(ctx context.Context, param JoinCmd) error {
	// cmd specify additional configuration for the join command
	// collected from defaults and/or computed values
	type cmd struct {
		InstallDir   string
		PrivateAddr  string
		DockerDevice string
		JoinCmd
	}

	dockerDevice := g.param.dockerDevice
	if g.param.storageDriver != constants.DeviceMapper {
		// Docker device is not used with non-devicemapper storage drivers
		dockerDevice = ""
	}

	var buf bytes.Buffer
	err := joinCmdTemplate.Execute(&buf, cmd{
		InstallDir:   g.installDir,
		PrivateAddr:  g.Node().PrivateAddr(),
		DockerDevice: dockerDevice,
		JoinCmd:      param,
	})
	if err != nil {
		return trace.Wrap(err, buf.String())
	}

	err = sshutils.Run(ctx, g.Client(), g.Logger(), buf.String(), nil)
	return trace.Wrap(err, param)
}

var joinCmdTemplate = template.Must(
	template.New("gravity_join").Parse(`
		cd {{.InstallDir}} && sudo ./gravity join {{.PeerAddr}} \
		--advertise-addr={{.PrivateAddr}} --token={{.Token}} --debug \
		--role={{.Role}} --docker-device={{.DockerDevice}} \
		--system-log-file=./telekube-system.log --state-dir={{.StateDir}} \
		--httpprofile=localhost:6061`))

// Leave makes given node leave the cluster
func (g *gravity) Leave(ctx context.Context, graceful Graceful) error {
	var cmd string
	if graceful {
		cmd = `leave --confirm`
	} else {
		cmd = `leave --confirm --force`
	}

	return trace.Wrap(g.runOp(ctx, cmd, nil))
}

// Remove ejects node from cluster
func (g *gravity) Remove(ctx context.Context, node string, graceful Graceful) error {
	var cmd string
	if graceful {
		cmd = fmt.Sprintf(`remove --confirm %s`, node)
	} else {
		cmd = fmt.Sprintf(`remove --confirm --force %s`, node)
	}
	return trace.Wrap(g.runOp(ctx, cmd, nil))
}

// Uninstall removes gravity installation. It requires Leave beforehand
func (g *gravity) Uninstall(ctx context.Context) error {
	cmd := fmt.Sprintf(`cd %s && sudo ./gravity system uninstall --confirm --system-log-file=./telekube-system.log`, g.installDir)
	err := sshutils.Run(ctx, g.Client(), g.Logger(), cmd, nil)
	return trace.Wrap(err, cmd)
}

// UninstallApp uninstalls the cluster application.
// This is usually required to properly clean up cloud resources
// internally managed by kubernetes in case of kubernetes cloud integration
func (g *gravity) UninstallApp(ctx context.Context) error {
	cmd := fmt.Sprintf("cd %s && sudo ./gravity app uninstall $(./gravity app-package) --system-log-file=./telekube-system.log", g.installDir)
	err := sshutils.Run(ctx, g.Client(), g.Logger(), cmd, nil)
	return trace.Wrap(err, cmd)
}

// PowerOff forcibly halts a machine
func (g *gravity) PowerOff(ctx context.Context, graceful Graceful) error {
	var cmd string
	if graceful {
		cmd = "sudo shutdown -h now"
	} else {
		cmd = "sudo poweroff -f"
	}

	err := sshutils.RunAndParse(ctx, g.Client(), g.Logger(), cmd, nil, nil)
	if err != nil {
		return trace.Wrap(err)
	}
	g.ssh = nil
	// TODO: reliably destinguish between force close of SSH control channel and command being unable to run
	return nil
}

func (g *gravity) Offline() bool {
	return g.ssh == nil
}

// Reboot gracefully restarts a machine and waits for it to become available again
func (g *gravity) Reboot(ctx context.Context, graceful Graceful) error {
	var cmd string
	if graceful {
		cmd = "sudo shutdown -r now"
	} else {
		cmd = "sudo reboot -f"
	}

	err := sshutils.RunAndParse(ctx, g.Client(), g.Logger(), cmd, nil, nil)
	if err != nil {
		return trace.Wrap(err)
	}

	// TODO: reliably destinguish between force close of SSH control channel and command being unable to run
	client, err := sshClient(ctx, g.Node(), g.Logger())
	if err != nil {
		return trace.Wrap(err, "SSH reconnect")
	}

	g.ssh = client
	return nil
}

// CollectLogs fetches system logs from the host into a local directory.
// prefix names the state sub-directory to store logs into. args specifies optional additional
// arguments to the report command.
// Returns the local path where the report files will be stored
func (g *gravity) CollectLogs(ctx context.Context, prefix string, args ...string) (localPath string, err error) {
	if g.ssh == nil {
		return "", trace.AccessDenied("cannot collect logs from an offline node %v", g)
	}

	localPath = filepath.Join(g.param.StateDir, "node-logs", prefix,
		fmt.Sprintf("%v-logs.tgz", g.Node().PrivateAddr()))
	return localPath, trace.Wrap(sshutils.PipeCommand(ctx, g.Client(), g.Logger(),
		fmt.Sprintf("cd %v && sudo ./gravity system report %v", g.installDir,
			strings.Join(args, " ")), localPath))
}

// SetInstaller transfers and prepares installer package given with installerUrl.
// The install directory will be overridden to the specified sub-directory
// in user's home
func (g *gravity) SetInstaller(ctx context.Context, installerURL string, subdir string) error {
	installDir := filepath.Join(g.param.homeDir, subdir)
	log := g.Logger().WithFields(logrus.Fields{"installer_url": installerURL, "installer_dir": installDir})

	log.Infof("Transfer installer %v -> %v.", installerURL, installDir)

	tgz, err := sshutils.TransferFile(ctx, g.Client(), log, installerURL, installDir, g.param.env)
	if err != nil {
		log.WithError(err).Warnf("Failed to transfer installer %v -> %v.", installerURL, installDir)
		return trace.Wrap(err)
	}

	err = sshutils.Run(ctx, g.Client(), log, fmt.Sprintf("tar -xvf %s -C %s", tgz, installDir), nil)
	if err != nil {
		return trace.Wrap(err)
	}

	g.installDir = installDir
	return nil
}

// TransferFile transfers the file specified with url into the given sub-directory
// subdir in user's home.
// The install directory will be overridden to the specified sub-directory
// in user's home
func (g *gravity) TransferFile(ctx context.Context, url, subdir string) error {
	dir := filepath.Join(g.param.homeDir, subdir)
	log := g.Logger().WithFields(logrus.Fields{"url": url, "dir": dir})

	log.Infof("Transfer %v -> %v.", url, dir)

	_, err := sshutils.TransferFile(ctx, g.Client(), log, url, dir, g.param.env)
	if err != nil {
		log.WithError(err).Warnf("Failed to transfer file %v -> %v.", url, dir)
		return trace.Wrap(err)
	}

	g.installDir = dir
	return nil
}

// ExecScript will transfer and execute script provided with given args
func (g *gravity) ExecScript(ctx context.Context, scriptUrl string, args []string) error {
	log := g.Logger().WithFields(logrus.Fields{
		"script": scriptUrl, "args": args})

	log.Debug("Execute.")

	spath, err := sshutils.TransferFile(ctx, g.Client(), log,
		scriptUrl, defaults.TmpDir, g.param.env)
	if err != nil {
		log.WithError(err).Error("failed to transfer script")
		return trace.Wrap(err)
	}

	err = sshutils.Run(ctx, g.Client(), log,
		fmt.Sprintf("sudo /bin/bash -x %s %s", spath, strings.Join(args, " ")), nil)
	return trace.Wrap(err)
}

// Upload uploads packages in current installer dir to cluster
func (g *gravity) Upload(ctx context.Context) error {
	err := sshutils.Run(ctx, g.Client(), g.Logger(), fmt.Sprintf(`cd %s && sudo ./upload`, g.installDir), nil)
	return trace.Wrap(err)
}

// Upgrade takes current installer and tries to perform upgrade
func (g *gravity) Upgrade(ctx context.Context) error {
	executablePath := filepath.Join(g.installDir, "gravity")
	return trace.Wrap(g.runOp(ctx,
		fmt.Sprintf("upgrade $(%v app-package --state-dir=%v) --etcd-retry-timeout=%v",
			executablePath,
			g.installDir,
			defaults.EtcdRetryTimeout),
		// Run update unattended (changed in 5.4).
		// Do this via the environment though to avoid breaking versions that
		// update in a non-blocking mode by default
		map[string]string{"GRAVITY_BLOCKING_OPERATION": "false"}))
}

// for cases when gravity doesn't return just opcode but an extended message
var reGravityExtended = regexp.MustCompile(`launched operation \"([a-z0-9\-]+)\".*`)

const (
	opStatusCompleted = "completed"
	opStatusFailed    = "failed"
)

// runOp launches specific command and waits for operation to complete, ignoring transient errors
func (g *gravity) runOp(ctx context.Context, command string, env map[string]string) error {
	var code string
	executablePath := filepath.Join(g.installDir, "gravity")
	logPath := filepath.Join(g.installDir, "telekube-system.log")
	err := sshutils.RunAndParse(ctx, g.Client(), g.Logger(),
		fmt.Sprintf(`sudo -E %v %v --insecure --quiet --system-log-file=%v`,
			executablePath, command, logPath),
		env, sshutils.ParseAsString(&code))
	if err != nil {
		return trace.Wrap(err)
	}
	if match := reGravityExtended.FindStringSubmatch(code); len(match) == 2 {
		code = match[1]
	}

	retry := wait.Retryer{
		Attempts:    1000,
		Delay:       time.Second * 20,
		FieldLogger: g.Logger().WithField("retry-operation", code),
	}

	err = retry.Do(ctx, func() error {
		var response string
		cmd := fmt.Sprintf(`cd %s && ./gravity status --operation-id=%s -q`, g.installDir, code)
		err := sshutils.RunAndParse(ctx, g.Client(), g.Logger(),
			cmd, nil, sshutils.ParseAsString(&response))
		if err != nil {
			return wait.Continue(cmd)
		}

		switch strings.TrimSpace(response) {
		case opStatusCompleted:
			return nil
		case opStatusFailed:
			return wait.Abort(trace.Errorf("%s: response=%s, err=%v", cmd, response, err))
		default:
			return wait.Continue("non-final / unknown op status: %q", response)
		}
	})
	return trace.Wrap(err)
}

// RunInPlanet executes given command inside Planet container
func (g *gravity) RunInPlanet(ctx context.Context, cmd string, args ...string) (string, error) {
	c := fmt.Sprintf(`cd %s && sudo ./gravity enter -- --notty %s -- %s`,
		g.installDir, cmd, strings.Join(args, " "))

	var out string
	err := sshutils.RunAndParse(ctx, g.Client(), g.Logger(), c, nil, sshutils.ParseAsString(&out))
	if err != nil {
		return "", trace.Wrap(err)
	}

	return out, nil
}

// IsLeader returns true if node is leader
func (g *gravity) IsLeader(ctx context.Context) bool {
	status, err := g.Status(ctx)
	if err != nil {
		return false
	}
	etcdLeaderKey := fmt.Sprintf("/planet/cluster/%s/master", status.Cluster.Cluster)
	// leaderIP, err := g.RunInPlanet(ctx, "planet", "leader", "view", fmt.Sprintf("--leader-key=%s", etcdLeaderKey))
	leaderIP, err := g.RunInPlanet(ctx, "etcdctl", "get", etcdLeaderKey)
	if err != nil {
		g.Logger().Errorf("failed to get leader: %v", err)
	}
	if leaderIP != g.Node().PrivateAddr() {
		return false
	}
	return true
}

// PartitionNetwork creates a network partition between this gravity node and
// the cluster.
func (g *gravity) PartitionNetwork(ctx context.Context, cluster []Gravity) error {
	for _, node := range cluster {
		if node != g {
			cmdDropInput := fmt.Sprintf("sudo iptables -I INPUT -s %s -j DROP", node.Node().PrivateAddr())
			if err := sshutils.Run(ctx, g.Client(), g.Logger(), cmdDropInput, nil); err != nil {
				return trace.Wrap(err, cmdDropInput)
			}
			cmdDropOutput := fmt.Sprintf("sudo iptables -I OUTPUT -s %s -j DROP", node.Node().PrivateAddr())
			if err := sshutils.Run(ctx, g.Client(), g.Logger(), cmdDropOutput, nil); err != nil {
				return trace.Wrap(err, cmdDropOutput)
			}
			g.Logger().WithFields(logrus.Fields{
				"dropInput":  cmdDropInput,
				"dropOutput": cmdDropOutput,
			}).Infof("Dropping packets to/from %s", node.Node().PrivateAddr())
		}
	}
	return nil
}

// UnpartitionNetwork removes network partition between this gravity node and
// the cluster. Will fail if network partition does not already exist.
func (g *gravity) UnpartitionNetwork(ctx context.Context, cluster []Gravity) error {
	// TODO: `iptables -D ...` will fail with 'Bad rule (does a matching rule exist
	// in that chain?)' when we attempt to delete a rule that doesn't exist. Either
	// verify the rule exists beforehand or use a different command?

	g.Logger().WithFields(logrus.Fields{
		"cluster": cluster,
		"g":       g,
	}).Info("UnpartitionNetwork")
	for _, node := range cluster {
		if node != g {
			cmdListRules := "sudo iptables -S"
			if err := sshutils.Run(ctx, g.Client(), g.Logger(), cmdListRules, nil); err != nil {
				return trace.Wrap(err, cmdListRules)
			}
			cmdAcceptInput := fmt.Sprintf("sudo iptables -D INPUT -s %s -j DROP", node.Node().PrivateAddr())
			if err := sshutils.Run(ctx, g.Client(), g.Logger(), cmdAcceptInput, nil); err != nil {
				return trace.Wrap(err, cmdAcceptInput)
			}
			cmdAcceptOutput := fmt.Sprintf("sudo iptables -D OUTPUT -s %s -j DROP", node.Node().PrivateAddr())
			if err := sshutils.Run(ctx, g.Client(), g.Logger(), cmdAcceptOutput, nil); err != nil {
				return trace.Wrap(err, cmdAcceptOutput)
			}
			g.Logger().WithFields(logrus.Fields{
				"acceptInput":  cmdAcceptInput,
				"acceptOutput": cmdAcceptOutput,
			}).Infof("Accepting packets to/from %s", node.Node().PrivateAddr())
		}
	}
	return nil
}

func asNodes(nodes []*gravity) (out Nodes) {
	out = make([]Gravity, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node)
	}
	return out
}

// String returns a textual representation of this list of nodes
func (r Nodes) String() string {
	nodes := make([]string, 0, len(r))
	for _, node := range r {
		nodes = append(nodes, node.String())
	}
	return strings.Join(nodes, ",")
}

// Nodes is a list of gravity nodes
type Nodes []Gravity
