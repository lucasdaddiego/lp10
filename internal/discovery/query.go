package discovery

import (
	"context"
	"net"
	"time"
)

// reply is one datagram a query drew, with the address it came from.
type reply struct {
	from net.IP
	data []byte
}

// queryOutcome says how a query window ended.
type queryOutcome int

const (
	queryTimedOut  queryOutcome = iota // the window ran out: pick among what came back
	queryStopped                       // onReply asked to stop — an early, endorsed match
	queryCancelled                     // ctx ended (a shutdown mid-window)
	queryNoSocket                      // nothing to send from
)

// query multicasts q to group out of every interface (openQuerySockets),
// resends it a couple of times within the window when resend is set (mDNS is
// lossy UDP; LSSDP answers the first datagram), and feeds every reply to
// onReply — on the calling goroutine, so the handler needs no lock — until it
// returns true, the window ends, or ctx ends. It is the one send/read/timer
// loop behind the mDNS, LSSDP and Spotify ZeroConf finders, so the three
// cannot drift apart.
func query(ctx context.Context, group string, q []byte, timeout time.Duration, resend bool, onReply func(reply) bool) queryOutcome {
	raddr, err := net.ResolveUDPAddr("udp4", group)
	if err != nil {
		return queryNoSocket
	}
	conns := openQuerySockets()
	if len(conns) == 0 {
		return queryNoSocket
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	sendAll := func() {
		for _, c := range conns {
			_, _ = c.WriteToUDP(q, raddr)
		}
	}
	sendAll()

	// One reader goroutine per socket funnels raw replies here. Closing done
	// unblocks a reader parked on the channel send when we return early;
	// closing the sockets (deferred above) unblocks one parked in ReadFromUDP.
	replies := make(chan reply, 64)
	done := make(chan struct{})
	defer close(done)
	spawnReaders(conns, replies, done)

	overall := time.NewTimer(timeout)
	defer overall.Stop()
	var tick <-chan time.Time
	if resend {
		t := time.NewTicker(timeout/3 + time.Millisecond)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case r := <-replies:
			if onReply(r) {
				return queryStopped
			}
		case <-tick:
			sendAll()
		case <-ctx.Done():
			return queryCancelled
		case <-overall.C:
			return queryTimedOut
		}
	}
}

// spawnReaders starts one goroutine per socket, funneling each reply (with its
// sender's IPv4) into replies until done closes or the socket does.
func spawnReaders(conns []*net.UDPConn, replies chan<- reply, done <-chan struct{}) {
	for _, c := range conns {
		go func(c *net.UDPConn) {
			buf := make([]byte, 9000)
			for {
				n, from, err := c.ReadFromUDP(buf)
				if n > 0 {
					r := reply{data: append([]byte(nil), buf[:n]...)}
					if from != nil {
						r.from = from.IP.To4()
					}
					select {
					case replies <- r:
					case <-done:
						return
					}
				}
				if err != nil {
					return
				}
			}
		}(c)
	}
}
