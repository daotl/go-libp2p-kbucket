package kbucket

import (
	"container/list"
	"math"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
)

// A helper struct to sort peers by their distance to the local node
type peerDistance struct {
	p        peer.ID
	distance ID
}

// peerDistanceSorter implements sort.Interface to sort peers by xor distance
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

// Append the peer.ID to the sorter's slice. It may no longer be sorted.
func (pds *peerDistanceSorter) appendPeer(p peer.ID, pDhtId ID) {
	pds.peers = append(pds.peers, peerDistance{
		p:        p,
		distance: xor(pds.target, pDhtId),
	})
}

// Append the peer.ID values in the list to the sorter's slice. It may no longer be sorted.
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
	out := make([]peer.ID, 0, sorter.Len())
	for _, p := range sorter.peers {
		out = append(out, p.p)
	}
	return out
}

// TCP + TLS1.3 : 4
// QUIC : 2
var PriorityFactor = 4.0

// A helper struct to sort peers by xor distance and latency
type peerCplLatency struct {
	p       peer.ID
	latency time.Duration
	cpl     int
}

// peerDistanceLatencySorter implements sort.Interface to sort peers by xor distance and latency
type peerDistanceLatencySorter struct {
	local      ID
	target     ID
	peers      []peerCplLatency
	bucketsize int
	metrics    peerstore.Metrics
	avgRTT     int64 // Nanoseconds
}

func (pdls *peerDistanceLatencySorter) Len() int { return len(pdls.peers) }
func (pdls *peerDistanceLatencySorter) Swap(a, b int) {
	pdls.peers[a], pdls.peers[b] = pdls.peers[b], pdls.peers[a]
}
func (pdls *peerDistanceLatencySorter) Less(a, b int) bool {
	if ConvertPeerID(pdls.peers[a].p).equal(pdls.local) {
		return true
	}
	if ConvertPeerID(pdls.peers[b].p).equal(pdls.local) {
		return false
	}

	// Calculate the number of improvements per step
	// reference: Stutzbach D, Rejaie R. Improving Lookup Performance Over a Widely-Deployed DHT[C]// 2006.
	avgBitsImprovedPerStep := 1.3327 + math.Log2(float64(pdls.bucketsize))

	p := float64(0)

	if pdls.avgRTT > 0 {
		p = (float64(pdls.peers[a].cpl-pdls.peers[b].cpl)/avgBitsImprovedPerStep)*PriorityFactor - float64(pdls.peers[a].latency.Nanoseconds()-pdls.peers[b].latency.Nanoseconds())/float64(pdls.avgRTT)
	} else {
		p = float64(pdls.peers[a].cpl-pdls.peers[b].cpl) / avgBitsImprovedPerStep
	}

	return p > 0
}

// Append the peer.ID to the sorter's slice. It may no longer be sorted.
func (pdls *peerDistanceLatencySorter) appendPeer(p peer.ID, pDhtId ID) {
	pdls.peers = append(pdls.peers, peerCplLatency{
		p:       p,
		latency: pdls.metrics.LatencyEWMA(p),
		cpl:     CommonPrefixLen(pdls.target, pDhtId),
	})
}

// Append the peer.ID values in the list to the sorter's slice. It may no longer be sorted.
func (pdls *peerDistanceLatencySorter) appendPeersFromList(l *list.List) {
	for e := l.Front(); e != nil; e = e.Next() {
		pdls.appendPeer(e.Value.(*PeerInfo).Id, e.Value.(*PeerInfo).dhtId)
	}
}

func (pdls *peerDistanceLatencySorter) sort() {
	sort.Sort(pdls)
}
