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

// The democracy binary connects to the Bitcoin network. The main difference
// between democracy and a regular Bitcoin node is that democracy provides a way
// for people to vote against Bitcoin - democracy rejects transactions and
// blocks, and favours peers that do not forward transactions, blocks, headers,
// etc.
package main

import (
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/wire"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// flags
var (
	dialSeeds     = flag.Bool("dial_seeds", true, "Dial the seed addresses; set to false (this significantly reduces effectiveness)")
	bitcoinPort   = flag.Int("btc_port", 8333, "Port for listening for bitcoin; set to 0 to disable inbound connections")
	httpPort      = flag.Int("http_port", 9333, "Port for HTTP listening; set to 0 to disable HTTP server")
	localAddr     = flag.String("local_addr", "", "ip:port combination to advertise to peers. If empty or unset, the host value is automatically detected using api.ipify.org and -btc_port is used for the port.")
	verbosity     = flag.Bool("v", false, "Log many more messages")
	inboundConns  = flag.Int("inbound_conns", 500, "Limit on outbound connection count")
	outboundConns = flag.Int("outbound_conns", 500, "Limit on outbound connection count")
)

// metrics
var (
	peersTotalMetric = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "peers_total",
			Help: "Number of peers in peer database",
		},
	)

	_ = promauto.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "inbound_connection_token_count",
			Help: "Number of tokens in the inbound connection token channel",
		},
		func() float64 {
			return float64(len(inboundTokens))
		},
	)
	_ = promauto.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "outbound_connection_token_count",
			Help: "Number of tokens in the outbound connection token channel",
		},
		func() float64 {
			return float64(len(outboundTokens))
		},
	)

	peersConnectedMetric = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "peers_connected",
			Help: "Number of peers connected, by direction (inbound/outbound)",
		},
		[]string{"direction"},
	)

	messagesReceivedMetric = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "messages_received",
			Help: "Counter of messages received, by command",
		},
		[]string{"command"},
	)

	messagesSentMetric = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "messages_sent",
			Help: "Counter of messages sent, by command",
		},
		[]string{"command"},
	)
)

// other
var (
	// for passing to others in addr messages; may be nil
	localNAMu sync.Mutex
	localNA   *wire.NetAddress

	// buffered channel which limits concurrent connections
	inboundTokens  chan struct{}
	outboundTokens chan struct{}

	// replaced with log.Printf if -v is passed
	logf = func(string, ...interface{}) {}
)

func main() {
	flag.Parse()

	inboundTokens = make(chan struct{}, *inboundConns)
	outboundTokens = make(chan struct{}, *outboundConns)

	if *verbosity {
		logf = log.Printf
	}

	if *dialSeeds {
		for _, seed := range chaincfg.MainNetParams.DNSSeeds {
			addr := net.JoinHostPort(seed.Host, chaincfg.MainNetParams.DefaultPort)
			ensurePeerInfo(addr)
		}
		go connectLoop()
	}

	if *bitcoinPort != 0 {
		go listenLoop()
	}

	if *httpPort != 0 {
		go collectMetricsLoop()
		http.Handle("/metrics", promhttp.Handler())
		log.Fatal(http.ListenAndServe(":"+strconv.Itoa(*httpPort), nil))
	}

	// Else block forever this way!
	select {}
}

// collectMetricsLoop periodically collects overall statistics such as
// connection count.
func collectMetricsLoop() {
	for range time.Tick(5 * time.Second) {
		// Collect metrics
		peersIn, peersOut := 0, 0
		peersMu.Lock()
		peersTotal := len(peers)
		for _, pi := range peers {
			pi.mu.Lock()
			if pi.peer != nil && pi.peer.Connected() {
				if pi.peer.Inbound() {
					peersIn++
				} else {
					peersOut++
				}
			}
			pi.mu.Unlock()
		}
		peersMu.Unlock()

		peersConnectedMetric.With(prometheus.Labels{"direction": "inbound"}).Set(float64(peersIn))
		peersConnectedMetric.With(prometheus.Labels{"direction": "outbound"}).Set(float64(peersOut))
		peersTotalMetric.Set(float64(peersTotal))
	}
}

// listenLoop figures out our place in the internet using ipify.org, then starts
// listening for inbound connections.
func listenLoop() {
	if *localAddr == "" {
		determineLocalNAFromIpify()
	} else {
		determineLocalNAFromFlag()
	}

	ln, err := net.Listen("tcp", ":"+strconv.Itoa(*bitcoinPort))
	if err != nil {
		log.Fatalf("Couldn't listen: %v", err)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatalf("Couldn't accept: %v", err)
		}
		select {
		case inboundTokens <- struct{}{}:
			// continue below
		default:
			logf("Closing inbound connection immediately due to connection limit: %v")
			conn.Close()
			continue
		}

		pi := &peerInfo{}
		p := peer.NewInboundPeer(pi.config())
		pi.peer = p

		// this is what AssociateConnection sets p.addr to
		addr := conn.RemoteAddr().String()
		peersMu.Lock()
		peers[addr] = pi
		peersMu.Unlock()

		p.AssociateConnection(conn)
		go func() {
			p.WaitForDisconnect()
			<-inboundTokens
		}()
	}
}

// determine a value for localNA from localAddr flag
func determineLocalNAFromFlag() {
	h, p, err := net.SplitHostPort(*localAddr)
	if err != nil {
		log.Fatalf("Invalid host:port value for -local_addr flag: %v", err)
		return
	}
	ip := net.ParseIP(h)
	if ip == nil {
		log.Fatalf("Couldn't parse IP address from -local_addr flag: %v", err)
		return
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		log.Fatalf("Couldn't parse port from -local_addr flag: %v", err)
		return
	}
	localNAMu.Lock()
	localNA = wire.NewNetAddressIPPort(ip, uint16(port), peerCfgTmpl.Services)
	localNAMu.Unlock()
}

// determine a value for localNA using ipify.
func determineLocalNAFromIpify() {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil {
		log.Printf("Couldn't get external IP address, continuing without self-adversiting: %v", err)
		return
	}
	defer resp.Body.Close()
	ipb, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Couldn't read IP address, continuing without self-adversiting: %v", err)
		return
	}
	ip := net.ParseIP(string(ipb))
	if ip == nil {
		log.Printf("Couldn't parse IP address, continuing without self-adversiting: %v", err)
		return
	}
	log.Printf("Local address for self-advertising: %v", ip)
	localNAMu.Lock()
	localNA = wire.NewNetAddressIPPort(ip, uint16(*bitcoinPort), peerCfgTmpl.Services)
	localNAMu.Unlock()
}

// connectLoop continually tries to connect to new peers, up to the outbound
// connection limit.
func connectLoop() {
	for {
		// wait until we can push a token
		outboundTokens <- struct{}{}
		var pi *peerInfo
		var addr string
		peersMu.RLock()
		for a, x := range peers {
			x.mu.Lock()
			if x.peer == nil || !x.peer.Connected() {
				addr, pi = a, x
			}
			x.mu.Unlock()
			if pi != nil {
				break
			}
		}
		peersMu.RUnlock()
		if pi == nil {
			time.Sleep(5 * time.Second)
			<-outboundTokens
			continue
		}
		go func() {
			// remove the token once done
			defer func() { <-outboundTokens }()
			logf("Dialing %v", addr)
			p, err := peer.NewOutboundPeer(pi.config(), addr)
			if err != nil {
				logf("Couldn't NewOutboundPeer: %v", err)
				return
			}
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				logf("Couldn't dial: %v", err)
				return
			}
			pi.mu.Lock()
			pi.peer = p
			pi.mu.Unlock()
			p.AssociateConnection(conn)
			p.WaitForDisconnect()
		}()
	}
}
