package main

import (
	"encoding/json"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

type streamConfig struct {
	maxMessages  int
	maxBytes     int
	idleTimeout  time.Duration
	sendInterval time.Duration
}

func parseStreamConfig(raw map[string]any) (streamConfig, error) {
	opts := streamConfig{maxMessages: 1000, maxBytes: 16 << 20}
	v, exists := raw["stream"]
	if !exists {
		return opts, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return opts, verr.New(verr.CodeConfig, "grpc stream must be an object")
	}
	for key, value := range m {
		var min int64
		max := int64(^uint(0) >> 1)
		switch key {
		case "maxMessages":
			min = 1
		case "maxBytes":
			min = 2 // An empty JSON array still takes two bytes.
		case "idleTimeoutMs", "sendIntervalMs":
			max = int64((1<<63 - 1) / time.Millisecond)
		default:
			return opts, verr.Newf(verr.CodeConfig, "grpc stream has unknown field %q", key)
		}
		b, err := json.Marshal(value)
		if err != nil {
			return opts, verr.Newf(verr.CodeConfig, "grpc stream.%s must be an integer", key)
		}
		n, err := strconv.ParseInt(string(b), 10, 64)
		if err != nil || n < min || n > max {
			return opts, verr.Newf(verr.CodeConfig, "grpc stream.%s must be an integer between %d and %d", key, min, max)
		}
		switch key {
		case "maxMessages":
			opts.maxMessages = int(n)
		case "maxBytes":
			opts.maxBytes = int(n)
		case "idleTimeoutMs":
			opts.idleTimeout = time.Duration(n) * time.Millisecond
		case "sendIntervalMs":
			opts.sendInterval = time.Duration(n) * time.Millisecond
		}
	}
	return opts, nil
}

func requestBody(raw map[string]any) ([]byte, error) {
	v, multiple := raw["messages"]
	if !multiple {
		return messageBytes(raw["message"])
	}
	if _, single := raw["message"]; single {
		return nil, verr.New(verr.CodeConfig, "grpc message and messages cannot both be set")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, verr.New(verr.CodeConfig, "grpc messages must be a list of JSON objects")
	}
	if _, err := messageList(b); err != nil {
		return nil, err
	}
	return b, nil
}

func messageList(body []byte) ([]json.RawMessage, error) {
	payload := bytesTrim(body)
	var messages []json.RawMessage
	if len(payload) == 0 || payload[0] != '[' || json.Unmarshal(payload, &messages) != nil {
		return nil, verr.New(verr.CodeConfig, "grpc messages must be a list of JSON objects")
	}
	for i, message := range messages {
		if err := requireJSONObject(message); err != nil {
			return nil, verr.Newf(verr.CodeConfig, "grpc messages[%d] must be a JSON object", i)
		}
	}
	return messages, nil
}

func decodeRequestMessages(md protoreflect.MethodDescriptor, req *spi.Request) ([]proto.Message, error) {
	_, multiple := req.Config["messages"]
	if !md.IsStreamingClient() {
		if multiple {
			return nil, verr.New(verr.CodeConfig, "grpc messages requires a client-streaming or bidi method; use message")
		}
		msg, err := decodeMessage(md, req.Body)
		return []proto.Message{msg}, err
	}
	if !multiple {
		return nil, verr.New(verr.CodeConfig, "grpc client-streaming and bidi methods require messages")
	}
	payloads, err := messageList(req.Body)
	if err != nil {
		return nil, err
	}
	messages := make([]proto.Message, 0, len(payloads))
	for i, payload := range payloads {
		msg, err := decodeMessage(md, payload)
		if err != nil {
			return nil, verr.Newf(verr.CodeConfig, "decode grpc messages[%d]: %s", i, err.Error())
		}
		messages = append(messages, msg)
	}
	return messages, nil
}
