package client

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
)

func DialWSS(host string, port int) (*websocket.Conn, *http.Response, error) {
	url := fmt.Sprintf("wss://%s:%d/websocket", host, port)

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec
		},
	}

	return dialer.Dial(url, nil)
}
