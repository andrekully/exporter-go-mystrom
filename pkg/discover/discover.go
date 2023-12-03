package discover

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/prometheus/common/log"
)

const port = ":7979"

type LabelsList map[string]string

type TargetsEntry struct {
	Targets []string   `json:"targets"`
	Labels  LabelsList `json:"labels"`
}
type TargetsList []TargetsEntry

type Packet struct {
	SourceIP   string           `json:"source_ip"`
	Port       int              `json:"port"`
	MacAddress net.HardwareAddr `json:"mac_address"`
	DeviceType int              `json:"device_type"`
}
type Packetlist map[string]Packet

var LocalAddress string
var discoverlist Packetlist
var connectionUDP *net.UDPConn

// Initialize -- starts the updater and listener goroutines on startup
func Initialize(localaddr string) {
	discoverlist = make(Packetlist)
	channel := make(chan Packet, 10)

	if strings.HasPrefix(localaddr, ":") {
		LocalAddress = getOutboundIP().String() + localaddr
	} else {
		LocalAddress = localaddr
	}
	localAddress, err := net.ResolveUDPAddr("udp", port)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	connectionUDP, err = net.ListenUDP("udp", localAddress)
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	go listen(channel, port, connectionUDP)
	go update(channel)
}

// ConnClose --
func ConnClose() {
	if err := connectionUDP.Close(); err != nil {
		log.Errorf("error: %v", err)
		return
	}
	log.Info("stopping discovery listener")
}

// Discover --
func Discover() ([]byte, error) {
	var targetlist TargetsList

	for macaddr, data := range discoverlist {
		targetlist = append(targetlist, TargetsEntry{
			Targets: []string{
				LocalAddress,
			},
			Labels: LabelsList{
				"instance":         data.SourceIP,
				"__metrics_path__": fmt.Sprintf("/device_by_mac/%s", data.MacAddress),
				"__mac_address":    macaddr,
				"__device_type":    fmt.Sprintf("%d", data.DeviceType),
			},
		})
	}

	return json.Marshal(targetlist)
}

// TargetByMacaddr --
func TargetByMacaddr(macaddr string) string {
	return discoverlist[macaddr].SourceIP
}

// update -- updates the
func update(channel <-chan Packet) {
	for {
		msg := <-channel
		log.Debugf("msg: %s | %s\n", msg.SourceIP, msg.MacAddress.String())
		discoverlist[msg.MacAddress.String()] = msg
	}
}

// listen -- listens for udp broadcast on the given port
func listen(receive chan Packet, port string, connection *net.UDPConn) {
	defer func() {
		log.Info("ending listenwe")
		connection.Close()
	}()

	var message Packet

	for {
		inputBytes := make([]byte, 4096)
		length, udpaddr, err := connection.ReadFromUDP(inputBytes)
		if err != nil {
			log.Errorf("error: %v", err)
			return
		}
		buffer := bytes.NewBuffer(inputBytes[:length])
		if len(buffer.String()) < 6 {
			continue
		}
		macString := net.HardwareAddr(buffer.String()[0:6])

		deviceType := int(buffer.String()[6])

		// fmt.Printf("msg: %s | %#v | %v\n", macString.String(), udpaddr.IP.String(), err)
		message = Packet{
			SourceIP:   udpaddr.IP.String(),
			Port:       udpaddr.Port,
			MacAddress: macString,
			DeviceType: deviceType,
		}

		receive <- message
	}
}

// getOutboundIP -- Get preferred outbound ip of this machine, connectivy not needed
func getOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP
}
