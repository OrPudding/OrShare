package proto

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

func Parse(text string) (*WebSocketMessage, error) {
	text = strings.TrimSpace(text)
	matches := reqPattern.FindStringSubmatch(text)
	if matches == nil {
		return nil, fmt.Errorf("invalid message format: %q", text)
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

func (m WebSocketMessage) Encode(newID *int) (string, error) {
	id := m.ID
	if newID != nil {
		id = *newID
	}

	sb := strings.Builder{}
	sb.WriteString(m.Type)
	sb.WriteString(":")
	sb.WriteString(strconv.Itoa(id))
	sb.WriteString(":")
	sb.WriteString(m.Name)

	if m.Payload != nil {
		b, err := json.Marshal(m.Payload)
		if err != nil {
			return "", err
		}
		sb.WriteString("?")
		sb.Write(b)
	}
	return sb.String(), nil
}

func MakeAck(req *WebSocketMessage, payload map[string]interface{}) *WebSocketMessage {
	return &WebSocketMessage{
		Type:    "ack",
		ID:      req.ID,
		Name:    req.Name,
		Payload: payload,
	}
}

// 对齐 CatShare：payload.taskId/type/reason/id
func MakeStatus(id int, taskId string, typ int, reason string) *WebSocketMessage {
	return &WebSocketMessage{
		Type: "action",
		ID:   id,
		Name: "status",
		Payload: map[string]interface{}{
			"taskId": taskId,
			"type":   typ,
			"reason": reason,
			"id":     taskId,
		},
	}
}
