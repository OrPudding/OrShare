package transport

import (
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func StartWebSocketServer(addr string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Upgrade error:", err)
			return
		}
		defer conn.Close()

		log.Println("WebSocket connected")

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("Read error:", err)
				return
			}

			text := string(message)
			log.Println("Raw:", text)

			parsed, err := ParseMessage(text)
			if err != nil {
				log.Println("Parse error:", err)
				continue
			}

			log.Printf("Parsed: Type=%s ID=%d Name=%s Payload=%v\n",
				parsed.Type, parsed.ID, parsed.Name, parsed.Payload)

			// 开发用：自动回一个 status=ok
			taskID := ""
			if parsed.Payload != nil {
				if v, ok := parsed.Payload["taskId"]; ok {
					taskID = fmt.Sprintf("%v", v)
				}
			}
			resp := fmt.Sprintf(
				"action:%d:status?{\"taskId\":\"%s\",\"type\":1,\"reason\":\"ok\"}",
				parsed.ID,
				taskID,
			)

			if err := conn.WriteMessage(websocket.TextMessage, []byte(resp)); err != nil {
				log.Println("Write error:", err)
				return
			}
		}
	})

	log.Println("WebSocket server listening on", addr)
	return http.ListenAndServe(addr, mux)
}
