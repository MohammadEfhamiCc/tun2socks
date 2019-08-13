package proxy

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/xjasonlyu/tun2socks/common/dns"
	"github.com/xjasonlyu/tun2socks/common/log"
	"github.com/xjasonlyu/tun2socks/common/lsof"
	"github.com/xjasonlyu/tun2socks/common/pool"
	"github.com/xjasonlyu/tun2socks/common/stats"
	"github.com/xjasonlyu/tun2socks/core"
	"github.com/xjasonlyu/tun2socks/proxy/socks"
)

type udpHandler struct {
	proxyHost string
	proxyPort int
	timeout   time.Duration

	remoteAddrMap sync.Map
	remoteConnMap sync.Map

	fakeDns       dns.FakeDns
	sessionStater stats.SessionStater
}

func NewUDPHandler(proxyHost string, proxyPort int, timeout time.Duration, fakeDns dns.FakeDns, sessionStater stats.SessionStater) core.UDPConnHandler {
	return &udpHandler{
		proxyHost:     proxyHost,
		proxyPort:     proxyPort,
		fakeDns:       fakeDns,
		sessionStater: sessionStater,
		timeout:       timeout,
	}
}

func (h *udpHandler) fetchUDPInput(conn core.UDPConn, input net.PacketConn, addr *net.UDPAddr) {
	buf := pool.BufPool.Get().([]byte)

	defer func() {
		h.Close(conn)
		pool.BufPool.Put(buf[:cap(buf)])
	}()

	for {
		input.SetDeadline(time.Now().Add(h.timeout))
		n, _, err := input.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
				log.Warnf("failed to read UDP data from remote: %v", err)
			}
			return
		}

		if _, err := conn.WriteFrom(buf[:n], addr); err != nil {
			log.Warnf("failed to write UDP data: %v", err)
			return
		}
	}
}

func (h *udpHandler) Connect(conn core.UDPConn, target *net.UDPAddr) error {
	if target == nil {
		log.Warnf("UDP target is invalid: %s", conn.LocalAddr().String())
		return errors.New("UDP target is invalid")
	}

	// Replace with a domain name if target address IP is a fake IP.
	var targetHost = target.IP.String()
	if h.fakeDns != nil {
		if host, exist := h.fakeDns.IPToHost(target.IP); exist {
			targetHost = host
		}
	}
	targetAddr := net.JoinHostPort(targetHost, strconv.Itoa(target.Port))
	if len(targetAddr) == 0 {
		return errors.New("target address is empty")
	}

	proxyAddr := net.JoinHostPort(h.proxyHost, strconv.Itoa(h.proxyPort))
	// Dial
	remoteConn, remoteAddr, err := socks.DialUDP(proxyAddr, targetAddr)
	if err != nil {
		log.Warnf("DialUDP %v error: %v", proxyAddr, err)
		return err
	}

	// Get name of the process.
	var process = lsof.GetProcessName(conn.LocalAddr())
	if h.sessionStater != nil {
		sess := &stats.Session{
			ProcessName:   process,
			Network:       conn.LocalAddr().Network(),
			DialerAddr:    remoteConn.LocalAddr().String(),
			ClientAddr:    conn.LocalAddr().String(),
			TargetAddr:    targetAddr,
			UploadBytes:   0,
			DownloadBytes: 0,
			SessionStart:  time.Now(),
		}
		h.sessionStater.AddSession(conn, sess)

		remoteConn = stats.NewSessionPacketConn(remoteConn, sess)
	}

	h.remoteAddrMap.Store(conn, remoteAddr)
	h.remoteConnMap.Store(conn, remoteConn)

	go h.fetchUDPInput(conn, remoteConn, target)

	log.Access(process, "proxy", "udp", conn.LocalAddr().String(), targetAddr)
	return nil
}

func (h *udpHandler) ReceiveTo(conn core.UDPConn, data []byte, addr *net.UDPAddr) error {
	var remoteAddr net.Addr
	var remoteConn net.PacketConn

	if value, ok := h.remoteConnMap.Load(conn); ok {
		remoteConn = value.(net.PacketConn)
	}

	if value, ok := h.remoteAddrMap.Load(conn); ok {
		remoteAddr = value.(net.Addr)
	}

	if remoteAddr == nil || remoteConn == nil {
		h.Close(conn)
		return errors.New(fmt.Sprintf("proxy connection %v->%v does not exists", conn.LocalAddr(), addr))
	}

	if _, err := remoteConn.WriteTo(data, remoteAddr); err != nil {
		h.Close(conn)
		return errors.New(fmt.Sprintf("write remote failed: %v", err))
	}

	return nil
}

func (h *udpHandler) Close(conn core.UDPConn) {
	conn.Close()

	if remoteConn, ok := h.remoteConnMap.Load(conn); ok {
		remoteConn.(net.PacketConn).Close()
		h.remoteConnMap.Delete(conn)
	}

	h.remoteAddrMap.Delete(conn)

	if h.sessionStater != nil {
		h.sessionStater.RemoveSession(conn)
	}
}