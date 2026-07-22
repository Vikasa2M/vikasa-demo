package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type Envelope struct {
	ID, Type, Source, Subject string
	Time                      time.Time
	Data                      []byte
}

func New(dot, district, cabinet, controller, ceType string, occurredAt time.Time, msg proto.Message) (*Envelope, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	service, event, err := ServiceEvent(ceType)
	if err != nil {
		return nil, err
	}
	subj, err := Subject(dot, district, cabinet, service, controller, event)
	if err != nil {
		return nil, err
	}
	source := fmt.Sprintf("//%s/%s/%s/%s", dot, district, cabinet, controller)
	h := sha256.New()
	h.Write([]byte(ceType))
	h.Write([]byte(source))
	h.Write([]byte(occurredAt.UTC().Format(time.RFC3339Nano)))
	h.Write(data)
	return &Envelope{
		ID:      hex.EncodeToString(h.Sum(nil)),
		Type:    ceType,
		Source:  source,
		Subject: subj,
		Time:    occurredAt.UTC(),
		Data:    data,
	}, nil
}

func (e *Envelope) Headers() nats.Header {
	h := nats.Header{}
	h.Set("ce-specversion", "1.0")
	h.Set("ce-id", e.ID)
	h.Set("ce-type", e.Type)
	h.Set("ce-source", e.Source)
	h.Set("ce-time", e.Time.Format(time.RFC3339Nano))
	h.Set("content-type", "application/protobuf")
	h.Set("Nats-Msg-Id", e.ID)
	return h
}

func FromMsg(m *nats.Msg) (*Envelope, error) {
	t, err := time.Parse(time.RFC3339Nano, m.Header.Get("ce-time"))
	if err != nil {
		return nil, fmt.Errorf("bad ce-time: %w", err)
	}
	e := &Envelope{
		ID: m.Header.Get("ce-id"), Type: m.Header.Get("ce-type"),
		Source: m.Header.Get("ce-source"), Subject: m.Subject,
		Time: t, Data: m.Data,
	}
	if e.ID == "" || e.Type == "" {
		return nil, fmt.Errorf("missing ce-id/ce-type headers")
	}
	return e, nil
}
