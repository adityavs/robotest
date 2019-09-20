package gravity

import (
	"context"

	"github.com/gravitational/robotest/lib/wait"
	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
)

// Failover isolates the current leader node and elects a new leader node.
// Conforms to ConfigFn interface.
func (c *TestContext) Failover(nodes []Gravity) error {
	// TODO: Configure timeouts
	ctx, cancel := context.WithTimeout(c.ctx, c.timeouts.Status)
	defer cancel()

	oldLeader, err := getLeaderNode(ctx, nodes)
	if err != nil {
		return trace.Wrap(err)
	}
	c.Logger().WithFields(logrus.Fields{
		"oldLeader": oldLeader,
	}).Info("Leader found")

	if err := oldLeader.PartitionNetwork(ctx); err != nil {
		return trace.Wrap(err, "failed to create network partition")
	}
	// NOTE: is subnets an appropriate name for the separate networks, or is the
	// term only relevant when grouping a range of IP addresses?
	subnets := make([][]Gravity, 0, 2)
	subnets[0] = []Gravity{oldLeader}
	for i, node := range nodes {
		if node == oldLeader {
			subnets[1] = append(nodes[:i], nodes[i+1:]...)
			break
		}
	}
	c.Logger().WithFields(logrus.Fields{
		"subnets": subnets,
	}).Info("Created network partition")

	retry := wait.Retryer{
		Attempts: leaderElectionRetries,
		Delay:    leaderElectionWait,
	}

	var newLeader Gravity
	err = retry.Do(ctx, func() error {
		newLeader, err = getLeaderNode(ctx, subnets[1])
		if err != nil || newLeader == oldLeader {
			return wait.Continue("new leader not yet elected", err)
		}
		return nil
	})
	if err != nil {
		return trace.Wrap(err, "new leader was not elected")
	}

	c.Logger().WithFields(logrus.Fields{
		"oldLeader": oldLeader,
		"newLeader": newLeader,
	}).Info("New leader elected")

	if err := oldLeader.UnpartitionNetwork(ctx); err != nil {
		return trace.Wrap(err, "failed to remove network partition")
	}
	c.Logger().Info("Removed network partition")

	retry = wait.Retryer{
		Attempts: activeStatusRetries,
		Delay:    activeStatusWait,
	}

	err = retry.Do(ctx, func() error {
		status := make([]*GravityStatus, 0, 2)
		status[0], err = newLeader.Status(ctx)
		if err != nil {
			return wait.Continue("status is unavailable on new leader", err)
		}

		status[1], err = oldLeader.Status(ctx)
		if err != nil {
			return wait.Continue("status is unavailable on old leader", err)
		}

		if status[0].Cluster.Status != status[1].Cluster.Status {
			c.Logger().Warnf("cluster status is not in sync: [%v, %v]", status[0], status[1])
			return wait.Continue("cluster status is not in sync")
		}

		// TODO: add Status.IsActive function
		if status[0].Cluster.Status != StatusActive {
			c.Logger().Warnf("cluster status is not active: %v", status[0])
			return wait.Continue("cluster status is not active")
		}
		return nil
	})
	return trace.Wrap(err)
}

// getLeaderNode returns the current leader node.
func getLeaderNode(ctx context.Context, nodes []Gravity) (leader Gravity, err error) {
	for _, node := range nodes {
		if node.IsLeader(ctx) {
			// TODO: is this check necessary, is it a reachable state where two
			// nodes think they are leader at the same time in the same cluster?
			if leader != nil {
				return nil, trace.BadParameter("multiple leader nodes [%v, %v]", leader, node)
			}
			leader = node
		}
	}
	if leader == nil {
		return nil, trace.NotFound("unable to get leader node")
	}
	return leader, nil
}