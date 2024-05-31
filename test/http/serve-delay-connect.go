package http

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/florianl/go-nfqueue"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/spf13/cobra"
)

const (
	defaultPort     = "8080"
	defaultDelay    = time.Duration(40 * time.Second)
	defaultQueueNum = 100
)

// NewServeDelayConnectCommand returns a command that starts an HTTP server
// and integrates with the kernel to delay connection acceptance.
func NewServeDelayConnectCommand() *cobra.Command {
	var command = &cobra.Command{
		Use:   "serve-delay-connect-test-server",
		Short: "Runs test http server which delays the connection accept and sends echo response.",
		Long:  "Runs test http server which delays the connection accept and sends echo response.",
		Run: func(cmd *cobra.Command, args []string) {
			queueNum, err := strconv.ParseUint(os.Getenv("QUEUE"), 10, 16)
			if err != nil {
				queueNum = defaultQueueNum
			}
			delay, err := time.ParseDuration(os.Getenv("DELAY"))
			if err != nil {
				delay = defaultDelay
			}
			port := os.Getenv("PORT")
			if len(port) == 0 {
				port = defaultPort
			}
			if err := serveDelayConnect(uint16(queueNum), delay, port); err != nil {
				log.Printf("Error received: %v\n", err)
			}
		},
	}

	return command
}

// serveDelayConnect registers a handler on the specified netfilter queue and
// starts an HTTP server on the given port. The handler delays SYN packet
// acceptance by the specified delay. Use the following iptables command to configure the handler:
// iptables -I INPUT -p tcp --dport 8080 -m conntrack --ctstate NEW -j NFQUEUE --queue-num 100
// or this one for ipv6 stack:
// ip6tables -I INPUT -p tcp --dport 8080 -m conntrack --ctstate NEW -j NFQUEUE --queue-num 100
func serveDelayConnect(queueNum uint16, delay time.Duration, port string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue, err := nfqueue.Open(&nfqueue.Config{
		NfQueue:      queueNum,
		MaxPacketLen: 0xFFFF,
		MaxQueueLen:  0xFF,
		Copymode:     nfqueue.NfQnlCopyPacket,
	})
	if err != nil {
		return fmt.Errorf("Could not open nfqueue: %w", err)
	}
	defer queue.Close()

	callback := func(a nfqueue.Attribute) int {
		log.Println("Callback is triggered")
		id := *a.PacketID
		payload := *a.Payload

		var tcp layers.TCP
		parser := gopacket.NewDecodingLayerParser(layers.LayerTypeIPv4, &layers.IPv4{}, &tcp)
		decoded := []gopacket.LayerType{}
		if err := parser.DecodeLayers(payload, &decoded); err != nil {
			log.Printf("Error decoding packet: %v\n", err)
			queue.SetVerdict(id, nfqueue.NfDrop)
			return 0
		}

		for _, layerType := range decoded {
			if layerType == layers.LayerTypeTCP {
				if tcp.SYN && !tcp.ACK {
					log.Printf("Delaying SYN packet for %s\n", delay.String())
					time.Sleep(delay)
					queue.SetVerdict(id, nfqueue.NfAccept)
					log.Println("SYN packet is accepted")
					return 0
				}
			}
		}

		log.Println("No SYN packets received")
		queue.SetVerdict(id, nfqueue.NfAccept)
		return 0
	}

	if err := queue.RegisterWithErrorFunc(ctx, callback, func(e error) int {
		log.Printf("Error reading from netlink socket: %v\n", e)
		return -1
	}); err != nil {
		return fmt.Errorf("Error registering callback: %w\n", err)
	}
	log.Printf("Callback is registered with delay %s\n", delay.String())

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.Proto)
	})
	log.Printf("Serving on %s\n", port)
	log.Println(http.ListenAndServe(":"+port, nil))
	return nil
}
