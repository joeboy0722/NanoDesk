package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
)

func main() {
	if len(os.Args) == 1 {
		if err := runServerGUI(); err != nil {
			startupError(err)
		}
		return
	}
	if err := runCLI(); err != nil {
		cliError(err)
		os.Exit(1)
	}
}
func runCLI() error {
	prepareCLI()
	s := serverSettings{ListenIP: "0.0.0.0", HTTPSPort: 443, TURNPort: 3478, TURNTLSPort: 5349, RelayMin: 49160, RelayMax: 49260, RelayMbps: 20, Certificate: "server.crt", PrivateKey: "server.key", Database: "nanodesk.db"}
	flag.Bool("cli", false, "use command-line mode (no arguments opens the Windows GUI)")
	listen := flag.String("listen", ":443", "HTTPS signaling address")
	flag.StringVar(&s.Certificate, "cert", s.Certificate, "TLS certificate PEM")
	flag.StringVar(&s.PrivateKey, "key", s.PrivateKey, "TLS private key PEM")
	flag.StringVar(&s.Database, "db", s.Database, "ID database")
	backup := flag.String("backup", "", "offline database snapshot destination")
	flag.StringVar(&s.PublicIP, "public-ip", "", "relay IPv4 override (required if -turn-host is omitted)")
	flag.StringVar(&s.Host, "turn-host", "", "server hostname or DDNS name")
	flag.IntVar(&s.TURNPort, "turn-port", s.TURNPort, "STUN/TURN UDP port")
	flag.IntVar(&s.TURNTLSPort, "turn-tls-port", s.TURNTLSPort, "TURN TLS port")
	flag.IntVar(&s.RelayMin, "relay-min", s.RelayMin, "minimum relay port")
	flag.IntVar(&s.RelayMax, "relay-max", s.RelayMax, "maximum relay port")
	flag.IntVar(&s.RelayMbps, "relay-mbps", s.RelayMbps, "bandwidth per allocation in Mbit/s")
	flag.Parse()
	if *backup != "" {
		return backupStoppedDatabase(s.Database, *backup)
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if host != "" {
		s.ListenIP = host
	}
	s.HTTPSPort, err = strconv.Atoi(port)
	if err != nil {
		return err
	}
	service, err := startServer(s)
	if err != nil {
		return err
	}
	defer service.Close()
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)
	defer signal.Stop(done)
	select {
	case <-done:
		return nil
	case err := <-service.done:
		return err
	}
}
func cliError(err error) { log.Print(fmt.Sprint(err)) }
