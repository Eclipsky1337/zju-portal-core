package atrust

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/internal/ping"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

const pingNum = 3

func getBestNodes(ctx context.Context, nodeGroups map[string][]string, dialContext func(context.Context, string, string) (net.Conn, error)) map[string]string {
	bestNodes := make(map[string]string)
	for group, nodes := range nodeGroups {
		if len(nodes) > 1 {
			var pingList []ping.TCPing
			var chList []<-chan struct{}

			for _, node := range nodes {
				parts := strings.Split(node, ":")
				host := parts[0]
				port, err := strconv.Atoi(parts[1])
				if err != nil {
					continue
				}

				tcping := ping.NewTCPing()
				tcping.SetContext(ctx)
				tcping.SetDialContext(dialContext)
				target := ping.Target{
					Host:     host,
					Port:     port,
					Counter:  pingNum,
					Interval: time.Duration(0.5 * float64(time.Second)),
					Timeout:  time.Duration(1 * float64(time.Second)),
				}
				tcping.SetTarget(&target)

				pingList = append(pingList, *tcping)
				ch := tcping.Start()
				chList = append(chList, ch)
			}

			for _, ch := range chList {
				<-ch
			}

			bestLatency := int64(0)
			bestNode := ""
			for i, tcping := range pingList {
				result := tcping.Result()
				if result.SuccessCounter == pingNum {
					latency := result.Avg().Milliseconds()

					if bestLatency == 0 || latency < bestLatency {
						bestNode = nodes[i]
						bestLatency = latency
					}
				}
			}

			if bestNode != "" {
				bestNodes[group] = bestNode
				log.Printf("Best node in group %s: %s with latency %d ms", group, bestNode, bestLatency)
			} else {
				log.Printf("No reachable node in group %s, using the first node", group)
				bestNodes[group] = nodes[0]
			}
		} else if len(nodes) == 1 {
			bestNodes[group] = nodes[0]
		}
	}

	return bestNodes
}

func (c *Client) updateBestNodes(ctx context.Context, updateBestNodesInterval int, handler func(map[string]string)) {
	ticker := time.NewTicker(time.Duration(updateBestNodesInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		bestNodes := getBestNodes(ctx, c.NodeGroups, c.underlayDialer.DialContext)
		if err := ctx.Err(); err != nil {
			return
		}
		c.BestNodesRWMutex.Lock()
		changed := !equalNodes(c.BestNodes, bestNodes)
		c.BestNodes = bestNodes
		c.BestNodesRWMutex.Unlock()
		if changed && handler != nil {
			handler(cloneNodes(bestNodes))
		}
	}
}

func (c *Client) bestNodes() map[string]string {
	c.BestNodesRWMutex.RLock()
	defer c.BestNodesRWMutex.RUnlock()
	return cloneNodes(c.BestNodes)
}

func cloneNodes(nodes map[string]string) map[string]string {
	result := make(map[string]string, len(nodes))
	for group, address := range nodes {
		result[group] = address
	}
	return result
}

func equalNodes(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for group, address := range left {
		if right[group] != address {
			return false
		}
	}
	return true
}
