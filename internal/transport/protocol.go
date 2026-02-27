package transport

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type WebSocketMessage struct {
	Type    string
	ID      int
	Name    string
	Payload map[string]interface{}
}

var reqPattern = regexp.MustCompile(`^([^:]+):(\d+):([^?]+)(\?(.*))?$`)

func ParseMessage(text string) (*WebSocketMessage, error) {
	text = strings.TrimSpace(text)

	matches := reqPattern.FindStringSubmatch(text)
	if matches == nil {
		return nil, fmt.Errorf("invalid message format")
	}

	id, err := strconv.Atoi(matches[2])
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if matches[5] != "" {
		if err := json.Unmarshal([]byte(matches[5]), &payload); err != nil {
			return nil, err
		}
	}

	return &WebSocketMessage{
		Type:    matches[1],
		ID:      id,
		Name:    matches[3],
		Payload: payload,
	}, nil
}
