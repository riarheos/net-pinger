package src

import (
	"fmt"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"net"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type remoteInfo struct {
	ip           net.IP
	addr         net.Addr
	isUp         bool
	pingsInState int
	gotReply     bool
}

type Ping struct {
	log           *zap.Logger   // logger
	ips           []net.IP      // the ip list to ping
	waitTimeout   time.Duration // a single ping wait deadline
	pauseDuration time.Duration // delay between pings
	aliveCount    uint8         // number of alive pings to consider host alive
	deadCount     uint8         // number of dead pings to consider host dead
	groupAlive    uint8         // number of alive hosts to consider whole setup alive
	groupDead     uint8         // number of alive hosts fo consider whole setup dead
	cmdAlive      string        // command to run when Alive
	cmdDead       string        // command to run when Dead

	conn         *icmp.PacketConn
	send         map[string]*remoteInfo
	pid          uint16
	seq          uint16
	totalAlive   int
	isTotalAlive bool
}

func NewPingFromCommandLine() (*Ping, error) {
	p := &Ping{}
	verbose := p.readArguments()

	var err error
	p.log = createLogger(verbose)
	if p.conn, err = icmp.ListenPacket("udp4", "0.0.0.0"); err != nil {
		return nil, err
	}

	// linux assigns local "port" to the id of the packets, need to account for that
	if runtime.GOOS == "linux" {
		addr := p.conn.IPv4PacketConn().LocalAddr().(*net.UDPAddr)
		p.pid = uint16(addr.Port)
	} else {
		p.pid = uint16(os.Getpid())
	}

	p.send = make(map[string]*remoteInfo)
	for _, ip := range p.ips {
		addr := &net.UDPAddr{IP: ip}
		p.send[ip.String()] = &remoteInfo{
			ip:           ip,
			addr:         addr,
			isUp:         false,
			pingsInState: 0,
		}
	}

	if p.groupAlive == 0 {
		p.groupAlive = uint8(len(p.ips))
	}

	p.log.Info("Starting the pinger",
		zap.Uint8("active on", p.groupAlive),
		zap.Uint8("dead on", p.groupDead))

	return p, nil
}

func (p *Ping) Run() error {
	recv := p.recv()

	for {
		p.seq++

		if err := p.sendRequests(); err != nil {
			return err
		}

		p.gatherResponses(recv)

		time.Sleep(p.pauseDuration)
	}
}

func (p *Ping) sendRequests() error {
	wm := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{
			ID:   int(p.pid),
			Seq:  int(p.seq),
			Data: []byte(""),
		},
	}
	wb, err := wm.Marshal(nil)
	if err != nil {
		return err
	}

	for _, ri := range p.send {
		ri.gotReply = false
		if _, err = p.conn.WriteTo(wb, ri.addr); err != nil {
			p.log.Error("Failed to send ICMP message", zap.Error(err))
		}
	}

	return nil
}

func (p *Ping) gatherResponses(recv chan icmpInfo) {

	timer := time.NewTimer(p.waitTimeout)

	for {
		select {
		case <-timer.C:
			timer.Stop()
			for ip, v := range p.send {
				if !v.gotReply {
					if v.isUp {
						v.isUp = false
						v.pingsInState = 1
					} else {
						v.pingsInState += 1
					}
					p.log.Debug("Ping timed out", zap.String("ip", ip), zap.Int("count", v.pingsInState))

					if v.pingsInState == int(p.deadCount) {
						p.log.Info("Remote host is dead", zap.String("ip", ip))
						p.handleHostDead()
					}
				}
			}
			return

		case i := <-recv:
			s := i.ip.String()
			v, ok := p.send[s]
			if !ok || uint16(i.echo.ID) != p.pid || uint16(i.echo.Seq) != p.seq {
				continue
			}

			v.gotReply = true
			if !v.isUp {
				v.isUp = true
				v.pingsInState = 1
			} else {
				v.pingsInState += 1
			}

			p.log.Debug("Successful ping", zap.String("ip", s), zap.Int("count", v.pingsInState))

			if v.pingsInState == int(p.aliveCount) {
				p.log.Info("Remote host is alive", zap.String("ip", s))
				p.handleHostAlive()
			}
		}
	}
}

func (p *Ping) handleHostAlive() {
	p.totalAlive += 1
	if !p.isTotalAlive && p.totalAlive >= int(p.groupAlive) {
		p.log.Info("Transitioning to alive state")
		p.runCommand(p.cmdAlive)
		p.isTotalAlive = true
	}
}

func (p *Ping) handleHostDead() {
	p.totalAlive -= 1
	if p.isTotalAlive && p.totalAlive <= int(p.groupDead) {
		p.log.Info("Transitioning to dead state")
		p.runCommand(p.cmdDead)
		p.isTotalAlive = false
	}
}

func (p *Ping) runCommand(command string) {
	p.log.Debug("Running command", zap.String("command", command))
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return
	}
	_ = cmd.Wait()
}

type icmpInfo struct {
	ip   net.IP
	echo icmp.Echo
}

func (p *Ping) recv() chan icmpInfo {
	ch := make(chan icmpInfo)

	go func() {
		rb := make([]byte, 1500)
		for {
			n, peer, err := p.conn.ReadFrom(rb)
			if err != nil {
				p.log.Error("Failed to receive ICMP message", zap.Error(err))
				continue
			}

			if n == 0 {
				close(ch)
				break
			}

			addr, ok := peer.(*net.UDPAddr)
			if !ok {
				p.log.Error("Failed to extract UDP address", zap.String("peer", peer.String()))
				continue
			}

			rm, err := icmp.ParseMessage(1, rb[:n])
			if err != nil {
				p.log.Error("Failed to parse ICMP message", zap.Error(err))
				continue
			}

			if rm.Type != ipv4.ICMPTypeEchoReply {
				continue
			}

			echo, ok := rm.Body.(*icmp.Echo)
			if !ok {
				p.log.Error("Failed to extract body from ICMP message", zap.String("peer", peer.String()))
				continue
			}

			ch <- icmpInfo{
				ip:   addr.IP,
				echo: *echo,
			}
		}
	}()

	return ch
}

func (p *Ping) readArguments() bool {
	generalOptions := pflag.NewFlagSet("General", pflag.ExitOnError)
	generalOptions.SortFlags = false
	verbose := generalOptions.BoolP("verbose", "v", false, "Enable verbose logging")
	generalOptions.StringVarP(&p.cmdAlive, "alive-cmd", "a", "", "Command to run when network is alive")
	generalOptions.StringVarP(&p.cmdDead, "dead-cmd", "d", "", "Command to run when network is dead")
	pflag.CommandLine.AddFlagSet(generalOptions)

	pingOptions := pflag.NewFlagSet("Ping", pflag.ExitOnError)
	pingOptions.SortFlags = false
	pingOptions.DurationVar(&p.waitTimeout, "wait", time.Second, "Single ping wait timeout")
	pingOptions.DurationVar(&p.pauseDuration, "pause", 5*time.Second, "Between ping pause duration")
	pingOptions.Uint8Var(&p.aliveCount, "alive-count", 3, "Number of alive pings to consider host alive")
	pingOptions.Uint8Var(&p.deadCount, "dead-count", 3, "Number of alive pings to consider host dead")
	pflag.CommandLine.AddFlagSet(pingOptions)

	groupOptions := pflag.NewFlagSet("Group", pflag.ExitOnError)
	groupOptions.SortFlags = false
	groupOptions.Uint8Var(&p.groupAlive, "group-alive", 0, "number of alive hosts to consider whole setup alive (default ip count)")
	groupOptions.Uint8Var(&p.groupDead, "group-dead", 0, "number of alive hosts to consider whole setup dead (default 0)")
	pflag.CommandLine.AddFlagSet(groupOptions)

	pflag.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "USAGE: %s [options] <ip> [<ip> ...]\n", os.Args[0])

		_, _ = fmt.Fprint(os.Stderr, "\nGeneral options:\n")
		generalOptions.PrintDefaults()

		_, _ = fmt.Fprint(os.Stderr, "\nPing options:\n")
		pingOptions.PrintDefaults()

		_, _ = fmt.Fprint(os.Stderr, "\nGrouping options:\n")
		groupOptions.PrintDefaults()
	}

	pflag.Parse()

	for _, arg := range pflag.Args() {
		ip := net.ParseIP(arg)
		if ip == nil {
			pflag.Usage()
		}
		p.ips = append(p.ips, ip)
	}

	if len(p.ips) == 0 {
		pflag.Usage()
	}

	return *verbose
}
