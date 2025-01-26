package main

import (
	"errors"
	"fmt"
	"github.com/spf13/pflag"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"net"
	"os"
	"os/exec"
	"time"
)

type PingerConfig struct {
	address     string
	sleepDelay  time.Duration
	waitTimeout time.Duration
	failCount   int
	good        string
	bad         string
}

type Pinger struct {
	PingerConfig
	addr net.Addr

	conn        *icmp.PacketConn
	pid         int
	seq         int
	remoteAlive bool
	fails       int
}

func NewPinger(cfg *PingerConfig) (*Pinger, error) {
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return nil, err
	}

	return &Pinger{
		PingerConfig: *cfg,
		addr:         &net.UDPAddr{IP: net.ParseIP(cfg.address)},

		conn: conn,
		pid:  os.Getpid() & 0xffff,
	}, nil
}

func (p *Pinger) Ping() error {
	p.seq++

	wm := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{
			ID:   p.pid,
			Seq:  p.seq,
			Data: []byte(""),
		},
	}
	wb, err := wm.Marshal(nil)
	if err != nil {
		return err
	}

	if _, err = p.conn.WriteTo(wb, p.addr); err != nil {
		return err
	}

	if err = p.conn.SetReadDeadline(time.Now().Add(p.waitTimeout)); err != nil {
		return err
	}
	rb := make([]byte, 1500)
	n, peer, err := p.conn.ReadFrom(rb)
	if err != nil {
		return err
	}

	if peer.String() != p.addr.String() {
		return errors.New("peer address mismatch")
	}

	rm, err := icmp.ParseMessage(1, rb[:n])
	if err != nil {
		return err
	}

	if rm.Type != ipv4.ICMPTypeEchoReply {
		return errors.New("invalid echo reply")
	}

	body, ok := rm.Body.(*icmp.Echo)
	if !ok {
		return errors.New("invalid echo reply body")
	}

	if body.Seq != p.seq || body.ID != p.pid {
		return errors.New("invalid echo reply contents")
	}

	return nil
}

func (p *Pinger) RunCommand(command string) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return
	}
	_ = cmd.Wait()
}

func (p *Pinger) Run() error {
	for {
		if err := p.Ping(); err != nil {
			p.fails++
			if p.fails == p.failCount {
				fmt.Printf("Remote is dead: %v\n", err)
				p.remoteAlive = false
				p.RunCommand(p.bad)
			}
		} else {
			if !p.remoteAlive {
				fmt.Println("Remote is alive")
				p.fails = 0
				p.remoteAlive = true
				p.RunCommand(p.good)
			}
		}

		time.Sleep(p.sleepDelay)
	}
}

func (p *Pinger) Close() {
	_ = p.conn.Close()
}

func main() {
	addr := pflag.StringP("address", "a", "", "address to ping")
	timeout := pflag.DurationP("timeout", "t", time.Second, "ping wait timeout")
	delay := pflag.DurationP("delay", "d", 5*time.Second, "ping delay")
	failCount := pflag.IntP("fail", "f", 3, "fail count")
	good := pflag.StringP("good", "g", "", "action to run on good")
	bad := pflag.StringP("bad", "b", "", "action to run on bad")

	pflag.Parse()

	if *addr == "" || *timeout == 0 || *delay == 0 || *good == "" || *bad == "" {
		pflag.Usage()
		os.Exit(1)
	}

	p, err := NewPinger(&PingerConfig{
		address:     *addr,
		sleepDelay:  *delay,
		waitTimeout: *timeout,
		failCount:   *failCount,
		good:        *good,
		bad:         *bad,
	})
	if err != nil {
		panic(err)
	}

	if err = p.Run(); err != nil {
		panic(err)
	}
}
