package kbucket

import (
	"container/list"
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

// A helper struct to sort peers by their distance to the local node,
// and the latency between the local node and each peer
type peerDistanceCplLatency struct {
	p        peer.ID
	distance ID
	// Is the local peer
	isLocal bool
	cpl     int
	latency time.Duration
}

// peerDistanceLatencySorter implements sort.Interface to sort peers by xor distance and latency
// Reference: Wang Xiang, "Design and Implementation of Low-latency P2P Network for Graph-Based Distributed Ledger" 2020.
// 202006 向往 - 面向图式账本的低延迟P2P网络的设计与实现.pdf
type peerDistanceLatencySorter struct {
	peers   []peerDistanceCplLatency
	metrics peerstore.Metrics
	local   ID
	target  ID
	// Estimated average number of bits improved per step.
	avgBitsImprovedPerStep float64
	// Estimated average numbter of round trip required per step.
	// Examples:
	// For TCP+TLS1.3, avgRoundTripPerStep = 4
	// For QUIC, avgRoundTripPerStep = 2
	avgRoundTripPerStep float64
	// Average latency of peers in nanoseconds
	avgLatency int64
}

func (pdls *peerDistanceLatencySorter) Len() int { return len(pdls.peers) }
func (pdls *peerDistanceLatencySorter) Swap(a, b int) {
	pdls.peers[a], pdls.peers[b] = pdls.peers[b], pdls.peers[a]
}
func (pdls *peerDistanceLatencySorter) Less(a, b int) bool {
	// only a is the local peer, a < b
	if pdls.peers[a].isLocal && !pdls.peers[b].isLocal {
		return true
	}
	// b is the local peer, a ≥ b
	if pdls.peers[b].isLocal {
		return false
	}

	// If avgLatency > 0, compare our priority score
	if pdls.avgLatency > 0 {
		deltaCpl := float64(pdls.peers[a].cpl - pdls.peers[b].cpl)
		deltaLatency := float64(pdls.peers[a].latency.Nanoseconds() - pdls.peers[b].latency.Nanoseconds())
		priority := deltaCpl*pdls.avgRoundTripPerStep/pdls.avgBitsImprovedPerStep - deltaLatency/float64(pdls.avgLatency)
		return priority > 0
	}

	// Otherwise, fall back to comparing distances to target
	return pdls.peers[a].distance.less(pdls.peers[b].distance)
}

// Append the peer.ID to the sorter's slice. It may no longer be sorted.
func (pdls *peerDistanceLatencySorter) appendPeer(p peer.ID, pDhtId ID) {
	pdls.peers = append(pdls.peers, peerDistanceCplLatency{
		p:        p,
		distance: xor(pdls.target, pDhtId),
		isLocal:  pDhtId.equal(pdls.local),
		cpl:      CommonPrefixLen(pdls.target, pDhtId),
		latency:  pdls.metrics.LatencyEWMA(p),
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

// Sort the given peers by their ascending distance from the target by considering both
// xor distance and latency. A new slice is returned.
func SortClosestPeersConsideringLatency(peers []peer.ID, metrics peerstore.Metrics, local, target ID, avgBitsImprovedPerStep, avgRoundTripPerStep float64, avgLatency int64) []peer.ID {
	sorter := peerDistanceLatencySorter{
		peers:                  make([]peerDistanceCplLatency, 0, len(peers)),
		metrics:                metrics,
		local:                  local,
		target:                 target,
		avgBitsImprovedPerStep: avgBitsImprovedPerStep,
		avgRoundTripPerStep:    avgRoundTripPerStep,
		avgLatency:             avgLatency,
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
