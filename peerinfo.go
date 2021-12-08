// Copyright 2021 The Democracy Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/wire"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// database of peers by address
	peersMu sync.RWMutex
	peers   = make(map[string]*peerInfo) // addr -> info

	// common config options
	peerCfgTmpl = peer.Config{
		UserAgentName:    "Democracy",
		UserAgentVersion: "1.0.0",
		ChainParams:      &chaincfg.MainNetParams,
		Services:         wire.SFNodeNetwork | wire.SFNodeWitness,
		TrickleInterval:  10 * time.Hour,
		DisableRelayTx:   false,
	}
)

// ensurePeerInfo puts a peerInfo into the peers map.
func ensurePeerInfo(addr string) *peerInfo {
	peersMu.Lock()
	pi := peers[addr]
	if pi == nil {
		// set up a new peerInfo, thus updating peers
		pi = &peerInfo{}
		peers[addr] = pi
	}
	peersMu.Unlock()
	return pi
}

// peerInfo tracks a single peer.
type peerInfo struct {
	mu     sync.Mutex
	peer   *peer.Peer
	noisy  bool
	height int32
}

// setNoisy flags the peer as noisy.
func (pi *peerInfo) setNoisy() {
	pi.mu.Lock()
	pi.noisy = true
	pi.mu.Unlock()
}

// config creates a config specific to this peer.
func (pi *peerInfo) config() *peer.Config {
	cfg := peerCfgTmpl
	cfg.Listeners = peer.MessageListeners{
		OnVersion:    pi.onVersion,
		OnVerAck:     pi.onVerAck,
		OnMemPool:    pi.onMemPool,
		OnTx:         pi.onTx,
		OnBlock:      pi.onBlock,
		OnInv:        pi.onInv,
		OnHeaders:    pi.onHeaders,
		OnGetData:    pi.onGetData,
		OnGetBlocks:  pi.onGetBlocks,
		OnGetHeaders: pi.onGetHeaders,
		OnGetAddr:    pi.onGetAddr,
		OnAddr:       pi.onAddr,
		OnRead: func(p *peer.Peer, bytesRead int, msg wire.Message, err error) {
			if msg != nil {
				messagesReceivedMetric.With(prometheus.Labels{"command": msg.Command()}).Inc()
			}
		},
		OnWrite: func(p *peer.Peer, bytesWritten int, msg wire.Message, err error) {
			if msg != nil {
				messagesSentMetric.With(prometheus.Labels{"command": msg.Command()}).Inc()
			}
		},
	}
	cfg.NewestBlock = pi.newestBlock
	return &cfg
}

// newestBlock reports a chain height. It can vary by peer; for inbound peers
// the height comes from the version message.
func (pi *peerInfo) newestBlock() (hash *chainhash.Hash, height int32, err error) {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	return nil, pi.height, nil
}

// onVersion records the peer's height as our height (to reflect back to this
// peer).
func (pi *peerInfo) onVersion(p *peer.Peer, msg *wire.MsgVersion) *wire.MsgReject {
	pi.mu.Lock()
	pi.height = msg.LastBlock
	pi.mu.Unlock()
	return nil
}

// onVerAck responds with getaddr.
func (pi *peerInfo) onVerAck(p *peer.Peer, msg *wire.MsgVerAck) {
	// Ask for more peers
	p.QueueMessage(wire.NewMsgGetAddr(), nil)
}

// onMemPool records the peer as noisy.
func (pi *peerInfo) onMemPool(p *peer.Peer, msg *wire.MsgMemPool) {
	logf("OnMemPool: %v", p)
	pi.setNoisy()
	// nothing in mempool
}

// onTx responds with a reject. Bitcoin has negative value to society, therefore
// any fee is insufficient for spending any energy accepting or relaying
// transactions.
func (pi *peerInfo) onTx(p *peer.Peer, msg *wire.MsgTx) {
	logf("OnTx: %v", p)
	hash := msg.TxHash()
	p.PushRejectMsg(wire.CmdTx, wire.RejectInsufficientFee, "rejected", &hash, false)
	pi.setNoisy()
}

// onBlock reponds with a reject. No difficulty is high enough to justify
// accepting or relaying blocks.
func (pi *peerInfo) onBlock(p *peer.Peer, msg *wire.MsgBlock, buf []byte) {
	logf("OnBlock: %v", p)
	hash := msg.BlockHash()
	p.PushRejectMsg(wire.CmdBlock, wire.RejectCheckpoint, "rejected", &hash, false)
	pi.setNoisy()
}

// onInv records the peer is noisy. inv? cool story bro.
func (pi *peerInfo) onInv(p *peer.Peer, msg *wire.MsgInv) {
	logf("OnInv: %v", p)
	pi.setNoisy()
}

// onHeaders records the peer is noisy. headers? grouse yarn mate.
func (pi *peerInfo) onHeaders(p *peer.Peer, msg *wire.MsgHeaders) {
	logf("OnHeaders: %v", p)
	pi.setNoisy()
}

// onGetData replies with notfound. data? what data? bitcoin? lol bitcoin is
// worthless dude
func (pi *peerInfo) onGetData(p *peer.Peer, msg *wire.MsgGetData) {
	logf("OnGetData: %v", p)
	pi.setNoisy()
	nf := wire.NewMsgNotFound()
	nf.InvList = msg.InvList
	p.QueueMessage(nf, nil)
}

// onGetBlocks replies with notfound. no blocks here buddy
func (pi *peerInfo) onGetBlocks(p *peer.Peer, msg *wire.MsgGetBlocks) {
	logf("OnGetBlocks: %v", p)
	pi.setNoisy()
	nf := wire.NewMsgNotFound()
	for _, h := range msg.BlockLocatorHashes {
		nf.AddInvVect(wire.NewInvVect(wire.InvTypeBlock, h))
	}
	p.QueueMessage(wire.NewMsgNotFound(), nil)
}

// onGetHeaders replies with notfound. yes we have no headers today
func (pi *peerInfo) onGetHeaders(p *peer.Peer, msg *wire.MsgGetHeaders) {
	logf("OnGetHeaders: %v", p)
	pi.setNoisy()
	nf := wire.NewMsgNotFound()
	for _, h := range msg.BlockLocatorHashes {
		nf.AddInvVect(wire.NewInvVect(wire.InvTypeBlock, h))
	}
	p.QueueMessage(wire.NewMsgNotFound(), nil)
}

// onGetAddr replies with a few addresses from the database.
func (pi *peerInfo) onGetAddr(p *peer.Peer, msg *wire.MsgGetAddr) {
	logf("OnGetAddr: %v", p)
	q := make([]*wire.NetAddress, 0, len(peers)+1)
	// add localNA if set, also update the timestamp.
	localNAMu.Lock()
	if localNA != nil {
		localNA.Timestamp = time.Now()
		q = append(q, localNA)
	}
	localNAMu.Unlock()

	peersMu.RLock()
	for _, pi := range peers {
		pi.mu.Lock()
		if pi.peer != nil && pi.peer.Connected() && !pi.noisy {
			q = append(q, pi.peer.NA())
		}
		pi.mu.Unlock()
	}
	peersMu.RUnlock()
	p.PushAddrMsg(q)
}

// onAddr adds new addresses to the peer database.
func (pi *peerInfo) onAddr(p *peer.Peer, msg *wire.MsgAddr) {
	logf("OnAddr: %v", p)
	// connect?
	for _, addr := range msg.AddrList {
		addr := net.JoinHostPort(addr.IP.String(), strconv.Itoa(int(addr.Port)))
		ensurePeerInfo(addr)
	}
}
