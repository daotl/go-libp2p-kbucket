package kbucket

import (
	"container/list"
	"errors"
	"math"
	"sort"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
)

const (
	AvgRoundTripsPerStepWithNewPeer_TCP_TLS = 4.0
	AvgRoundTripsPerStepWithNewPeer_QUIC    = 2.0
)

// peerSorter sorts the peers with a certain strategy
type peerSorter interface {
	sort.Interface

	// peers returns thee first `count` peer IDs or all if `count` is 0 or there aren't enough in the peerSorter.
	getPeers(count int) []peer.ID

	// appendPeer appends the peer.ID to the sorter's slice. It may no longer be sorted.
	appendPeer(p peer.ID, pDhtId ID)

	// appendPeersFromList appends the peer.ID values in the list to the sorter's slice.
	// It may no longer be sorted.
	appendPeersFromList(l *list.List)

	// sort the peers in the peerSorter.
	sort()
}

// A helper struct to sort peers by their distance to the local node
type peerDistance struct {
	p        peer.ID
	distance ID
}

// peerDistanceSorter implements peerSorter to sort peers by xor distance
type peerDistanceSorter struct {
	peers  []peerDistance
	target ID
}

func (pds *peerDistanceSorter) Len() int { return len(pds.peers) }
func (pds *peerDistanceSorter) Swap(a, b int) {
	pds.peers[a], pds.peers[b] = pds.peers[b], pds.peers[a]
}
func (pds *peerDistanceSorter) Less(a, b int) bool {
	return pds.peers[a].distance.less(pds.peers[b].distance)
}

func (pds *peerDistanceSorter) getPeers(count int) []peer.ID {
	if 0 < count && count < pds.Len() {
		pds.peers = pds.peers[:count]
	}
	out := make([]peer.ID, 0, pds.Len())
	for _, p := range pds.peers {
		out = append(out, p.p)
	}
	return out
}

func (pds *peerDistanceSorter) appendPeer(p peer.ID, pDhtId ID) {
	pds.peers = append(pds.peers, peerDistance{
		p:        p,
		distance: xor(pds.target, pDhtId),
	})
}

func (pds *peerDistanceSorter) appendPeersFromList(l *list.List) {
	for e := l.Front(); e != nil; e = e.Next() {
		pds.appendPeer(e.Value.(*PeerInfo).Id, e.Value.(*PeerInfo).dhtId)
	}
}

func (pds *peerDistanceSorter) sort() {
	sort.Sort(pds)
}

// Sort the given peers by their ascending distance from the target. A new slice is returned.
func SortClosestPeers(peers []peer.ID, target ID) []peer.ID {
	sorter := peerDistanceSorter{
		peers:  make([]peerDistance, 0, len(peers)),
		target: target,
	}
	for _, p := range peers {
		sorter.appendPeer(p, ConvertPeerID(p))
	}
	sorter.sort()
	return sorter.getPeers(0)
}

/* #DAOT: peerDistanceAndLatencySorter */

// A helper struct to sort peers by their distance (CPL) and latency (measured by RTT) to the local node
type peerPriority struct {
	p peer.ID
	// cpl int
	// Derived from `p` from the formula from p.37, section 3.7.3 of the below paper.
	// Indicates the relative amount of expected RTTs saved by selecting the peer to query, larger is better.
	priority float64
}

// peerDistanceAndLatencySorter implements peerSorter to sort peers by taking into account both the
// xor distance to the target and the latency to the local peer.
// For algorithm details, see section 3.7.3 of the paper "Design and Implementation of Low-latency P2P Network for Graph-Based Distributed Ledger" by Wang Xiang.
// "面向图式账本的低延迟P2P 网络的设计与实现" by 向往
type peerDistanceAndLatencySorter struct {
	peers []peerPriority
	// Used to get RTTs of peers to the local peer
	metrics peerstore.Metrics
	// Optional: Used to get connectedness of peers to the local peer
	dialer                 network.Dialer
	local, target          ID
	localCpl               int
	avgBitsImprovedPerStep float64
	// Average RTT count needed each step to connect to a new peer in the lookup process,
	// varies among transport protocols, reference values:
	// For TCP+TLS1.3 : 4
	// For QUIC : 2
	avgRoundTripsPerStepWithNewPeer float64
	// Average RTT of all peers in the RoutingTable to the local peer in microseconds.
	avgPeerRTTMicroSecs int64
}

func newPeerDistanceAndLatencySorter(
	peers []peerPriority,
	metrics peerstore.Metrics,
	dialer network.Dialer,
	local ID,
	target ID,
	avgBitsImprovedPerStep float64,
	avgRoundTripsPerStepWithNewPeer float64,
	avgPeerRTTMicroSecs int64,
) (*peerDistanceAndLatencySorter, error) {
	if peers == nil {
		peers = make([]peerPriority, 0)
	}
	// peerstore.Metrics must exist for this strategy.
	if metrics == nil {
		return nil, errors.New("metrics cannot be nil for peerDistanceAndLatencySorter")
	}
	if avgBitsImprovedPerStep <= 0 {
		return nil, errors.New("avgBitsImprovedPerStep must be > 0")
	}
	// If not set will default to 4 (TCP+TLS1.3 settings) which will value the xor distance more in sorting.
	if avgRoundTripsPerStepWithNewPeer <= 0 {
		avgRoundTripsPerStepWithNewPeer = AvgRoundTripsPerStepWithNewPeer_TCP_TLS
	}
	if avgPeerRTTMicroSecs <= 0 {
		return nil, errors.New("avgPeerRTTMicroSecs must be > 0")
	}
	return &peerDistanceAndLatencySorter{
		peers:                           peers,
		metrics:                         metrics,
		dialer:                          dialer,
		local:                           local,
		target:                          target,
		localCpl:                        CommonPrefixLen(local, target),
		avgBitsImprovedPerStep:          avgBitsImprovedPerStep,
		avgRoundTripsPerStepWithNewPeer: avgRoundTripsPerStepWithNewPeer,
		avgPeerRTTMicroSecs:             avgPeerRTTMicroSecs,
	}, nil
}

func (pdls *peerDistanceAndLatencySorter) Len() int { return len(pdls.peers) }
func (pdls *peerDistanceAndLatencySorter) Swap(a, b int) {
	pdls.peers[a], pdls.peers[b] = pdls.peers[b], pdls.peers[a]
}
func (pdls *peerDistanceAndLatencySorter) Less(a, b int) bool {
	peerA, peerB := pdls.peers[a], pdls.peers[b]

	// If only the CPL of one of the peers is less then the local peer, prioritize the other peer.
	// if peerA.cpl < pdls.localCpl && peerB.cpl >= pdls.localCpl {
	// 	return false
	// } else if peerA.cpl >= pdls.localCpl && peerB.cpl < pdls.localCpl {
	// 	return true
	// }

	return peerA.priority > peerB.priority
}

func (pdls *peerDistanceAndLatencySorter) getPeers(count int) []peer.ID {
	if 0 < count && count < pdls.Len() {
		pdls.peers = pdls.peers[:count]
	}
	out := make([]peer.ID, 0, pdls.Len())
	for _, p := range pdls.peers {
		out = append(out, p.p)
	}
	return out
}

func (pdls *peerDistanceAndLatencySorter) appendPeer(p peer.ID, pDhtId ID) {
	cpl := CommonPrefixLen(pdls.target, pDhtId)
	rttMicroSecs := pdls.metrics.LatencyEWMA(p).Microseconds()

	// If the peer is already connected to the local peer, only one RTT is needed to query it,
	// so (avgRoundTripsPerStepWithNewPeer - 1) RTTs can be saved
	n := 0.0
	if pdls.dialer != nil {
		if pdls.dialer.Connectedness(p) == network.Connected {
			n = math.Max(pdls.avgRoundTripsPerStepWithNewPeer-1, 0)
		}
	}

	pdls.peers = append(pdls.peers, peerPriority{
		p: p,
		// cpl: CommonPrefixLen(pdls.target, pDhtId),
		priority: float64(cpl)*pdls.avgRoundTripsPerStepWithNewPeer/pdls.avgBitsImprovedPerStep -
			float64(rttMicroSecs)/float64(pdls.avgPeerRTTMicroSecs) + n,
	})
}

func (pdls *peerDistanceAndLatencySorter) appendPeersFromList(l *list.List) {
	for e := l.Front(); e != nil; e = e.Next() {
		pdls.appendPeer(e.Value.(*PeerInfo).Id, e.Value.(*PeerInfo).dhtId)
	}
}

func (pdls *peerDistanceAndLatencySorter) sort() {
	sort.Sort(pdls)
}

// Sort the given peers by their ascending closeness by taking into account both the distance to the target
// and the latency (measured by RTT) to the local peer. Fallback to SortClosestPeers if `metrics` is nil.
// A new slice is returned.
func SortClosestPeersByDistanceAndLatency(
	peers []peer.ID,
	metrics peerstore.Metrics,
	dialer network.Dialer,
	local, target ID,
	avgBitsImprovedPerStep float64,
	avgRoundTripsPerStepWithNewPeer float64,
	avgPeerRTTMicroSecs int64,
) ([]peer.ID, error) {
	sorter, err := newPeerDistanceAndLatencySorter(
		make([]peerPriority, 0, len(peers)),
		metrics,
		dialer,
		local,
		target,
		avgBitsImprovedPerStep,
		avgRoundTripsPerStepWithNewPeer,
		avgPeerRTTMicroSecs,
	)
	if err != nil {
		return nil, err
	}

	for _, p := range peers {
		sorter.appendPeer(p, ConvertPeerID(p))
	}
	sorter.sort()
	return sorter.getPeers(0), nil
}

// EstimatedAvgBitsImprovedPerStepFromBucketSize calculates the estimated average number of bits improved per lookup step.
// Reference: D. Stutzbach and R. Rejaie, "Improving Lookup Performance Over a Widely-Deployed DHT," Proceedings IEEE INFOCOM 2006.
// For the basic Kademlia approach D(1,1,k), m(1,k) approaches log(2,k)+0.3327, the number of bits improved on average is 1+m(1,k)=1.3327+log(2,k)
func EstimatedAvgBitsImprovedPerStepFromBucketSize(bucketSize int) float64 {
	return 1.3327 + math.Log2(float64(bucketSize))
}
