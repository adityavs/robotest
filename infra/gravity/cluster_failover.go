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
		"leader": oldLeader,
	}).Info("Initial leader node")

	if err := oldLeader.PartitionNetwork(ctx, nodes); err != nil {
		return trace.Wrap(err, "failed to create network partition")
	}

	partitions := getPartitions(nodes, oldLeader)
	c.Logger().WithFields(logrus.Fields{
		"partitions": partitions,
	}).Info("Created network partition")

	retry := wait.Retryer{
		Attempts: leaderElectionRetries,
		Delay:    leaderElectionWait,
	}
	if err = retry.Do(ctx, retryNewLeaderElected(c, partitions[1], oldLeader)); err != nil {
		return trace.Wrap(err, "new leader was not elected")
	}
	newLeader, err := getLeaderNode(ctx, partitions[1])
	if err != nil {
		return trace.Wrap(err)
	}
	c.Logger().WithFields(logrus.Fields{
		"oldLeader": oldLeader,
		"newLeader": newLeader,
	}).Info("New leader elected")

	if err := c.Status(partitions[1]); err != nil {
		return trace.Wrap(err, "cluster partition is nonoperational")
	}

	c.Logger().WithFields(logrus.Fields{
		"nodes": nodes,
	}).Infof("Current cluster")

	if err := oldLeader.UnpartitionNetwork(ctx, nodes); err != nil {
		return trace.Wrap(err, "failed to remove network partition")
	}
	c.Logger().Info("Removed network partition")

	retry = wait.Retryer{
		Attempts: activeStatusRetries,
		Delay:    activeStatusWait,
	}
	err = retry.Do(ctx, retryClusterIsActive(c, oldLeader, newLeader))

	return trace.Wrap(err)
}

// retryNewLeaderElected returns a retry function. Verifies that a new leader
// has been elected.
func retryNewLeaderElected(c *TestContext, cluster []Gravity, oldLeader Gravity) (retryFunc func() error) {
	return func() error {
		ctx, cancel := context.WithTimeout(c.ctx, c.timeouts.Status)
		defer cancel()

		newLeader, err := getLeaderNode(ctx, cluster)
		if err != nil || newLeader == oldLeader {
			return wait.Continue("new leader not yet elected")
		}
		return nil
	}
}

// retryClusterIsActive returns a retry function. This function verifies that
// the oldLeader and newLeader status are synchronized and that they are active.
func retryClusterIsActive(c *TestContext, oldLeader, newLeader Gravity) (retryFunc func() error) {
	return func() error {
		var err error
		var status [2]*GravityStatus

		ctx, cancel := context.WithTimeout(c.ctx, c.timeouts.Status)
		defer cancel()

		status[0], err = newLeader.Status(ctx)
		if err != nil {
			return wait.Continue("status is unavailable on new leader: %v", err)
		}

		status[1], err = oldLeader.Status(ctx)
		if err != nil {
			return wait.Continue("status is unavailable on old leader: %v", err)
		}

		if status[0].Cluster.Status != status[1].Cluster.Status {
			c.Logger().Warnf("cluster status is not in sync: [%v, %v]", status[0], status[1])
			return wait.Continue("cluster status is not in sync")
		}

		if status[0].Cluster.Status != StatusActive {
			c.Logger().Warnf("cluster status is not active: %v", status[0])
			return wait.Continue("cluster status is not active")
		}
		return nil
	}
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

// getPartitions returns the two network partitions created when
// isolating leader from the cluster.
func getPartitions(cluster []Gravity, leader Gravity) (partitions [2][]Gravity) {
	partitions[0] = []Gravity{leader}
	for i, node := range cluster {
		if node == leader {
			partitions[1] = append(cluster[:i], cluster[i+1:]...)
			break
		}
	}
	return partitions
}
