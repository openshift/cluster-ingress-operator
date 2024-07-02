package http

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

const (
	defaultPort    = "8080"
	defaultTimeout = time.Duration(1 * time.Second)
)

func NewServeWebSocketCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve-ws-test-server",
		Short: "serve WebSocket test server",
		Long:  "serve-inactive-test-server runs a WebSocket test server which sends echo response.",
		Run: func(cmd *cobra.Command, args []string) {
			port := os.Getenv("PORT")
			if len(port) == 0 {
				port = defaultPort
			}
			timeout, err := time.ParseDuration(os.Getenv("TIMEOUT"))
			if err != nil {
				timeout = defaultTimeout
			}
			serveWebSocket(port, timeout)
		},
	}
}

func serveWebSocket(port string, timeout time.Duration) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		notify := w.(http.CloseNotifier).CloseNotify()
		go func() {
			<-notify
			log.Printf("Client connection closed\n")
		}()

		upgrader := websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade connection: %v\n", err)
			return
		}
		defer conn.Close()

		msgType, p, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Failed to read the message: %v\n", err)
			return
		}
		log.Printf("Received message: %s\n", p)

		time.Sleep(timeout)

		err = conn.WriteMessage(msgType, p)
		if err != nil {
			log.Printf("Failed to write the response: %v\n", err)
			return
		}
		log.Printf("Sent message: %s\n", p)
	})

	log.Printf("Listening on port %v\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
