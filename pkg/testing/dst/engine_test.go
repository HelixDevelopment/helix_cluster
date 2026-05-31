package dst

import (
	"testing"
	"time"
)

func TestNewEngine(t *testing.T) {
	eng := NewEngine(42)
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
	if eng.Seed() != 42 {
		t.Errorf("expected seed 42, got %d", eng.Seed())
	}
}

func TestRegisterNode(t *testing.T) {
	eng := NewEngine(1)
	eng.RegisterNode("n1", "10.0.0.1:7946")
	if eng.NodeCount() != 1 {
		t.Errorf("expected 1 node, got %d", eng.NodeCount())
	}
	n, ok := eng.GetNode("n1")
	if !ok {
		t.Fatal("expected node n1 to exist")
	}
	if n.Address != "10.0.0.1:7946" {
		t.Errorf("expected address 10.0.0.1:7946, got %s", n.Address)
	}
	if !n.Online {
		t.Error("expected node to be online")
	}
}

func TestListNodes(t *testing.T) {
	eng := NewEngine(1)
	eng.RegisterNode("b", "10.0.0.2:7946")
	eng.RegisterNode("a", "10.0.0.1:7946")
	nodes := eng.ListNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].ID != "a" || nodes[1].ID != "b" {
		t.Error("expected nodes sorted by ID")
	}
}

func TestSendMessage(t *testing.T) {
	eng := NewEngine(1)
	var received *Message
	eng.RegisterHandler("message", func(e *Event) {
		received = e.Payload.(*Message)
	})

	msg := &Message{From: "a", To: "b", Payload: []byte("hello")}
	id := eng.SendMessage(msg, 100)
	if id != 1 {
		t.Errorf("expected event id 1, got %d", id)
	}

	processed := eng.Run(10)
	if processed != 1 {
		t.Errorf("expected 1 event processed, got %d", processed)
	}
	if received == nil {
		t.Fatal("expected message to be delivered")
	}
	if string(received.Payload) != "hello" {
		t.Errorf("expected payload hello, got %s", string(received.Payload))
	}
}

func TestDeliverMessage(t *testing.T) {
	eng := NewEngine(1)
	var received *Message
	eng.RegisterHandler("message", func(e *Event) {
		received = e.Payload.(*Message)
	})

	msg := &Message{From: "a", To: "b", Payload: []byte("direct")}
	eng.DeliverMessage(msg)
	if received == nil {
		t.Fatal("expected immediate delivery")
	}
	if string(received.Payload) != "direct" {
		t.Errorf("expected payload direct, got %s", string(received.Payload))
	}
}

func TestScheduleEvent(t *testing.T) {
	eng := NewEngine(1)
	var got string
	eng.RegisterHandler("custom", func(e *Event) {
		got = e.Payload.(string)
	})

	eng.ScheduleEvent("custom", "payload", 50)
	eng.Run(10)
	if got != "payload" {
		t.Errorf("expected payload, got %s", got)
	}
}

func TestRun_MaxEvents(t *testing.T) {
	eng := NewEngine(1)
	eng.RegisterHandler("message", func(e *Event) {})

	for i := 0; i < 5; i++ {
		eng.SendMessage(&Message{From: "a", To: "b"}, int64(i+1)*10)
	}

	processed := eng.Run(3)
	if processed != 3 {
		t.Errorf("expected 3 processed, got %d", processed)
	}
	if eng.events.Len() != 2 {
		t.Errorf("expected 2 remaining events, got %d", eng.events.Len())
	}
}

func TestNow(t *testing.T) {
	eng := NewEngine(1)
	if !eng.Now().Equal(time.Unix(0, 0)) {
		t.Errorf("expected zero virtual time, got %v", eng.Now())
	}
	eng.ScheduleEvent("noop", nil, 1e9)
	eng.Run(1)
	if !eng.Now().Equal(time.Unix(1, 0)) {
		t.Errorf("expected 1s virtual time, got %v", eng.Now())
	}
}

func TestDeterminism(t *testing.T) {
	var results []int
	for i := 0; i < 2; i++ {
		eng := NewEngine(12345)
		sum := 0
		eng.RegisterHandler("msg", func(e *Event) {
			sum += int(eng.RNG().Int31n(100))
		})
		for j := 0; j < 10; j++ {
			eng.ScheduleEvent("msg", nil, int64(j+1))
		}
		eng.Run(100)
		results = append(results, sum)
	}
	if results[0] != results[1] {
		t.Errorf("expected deterministic results, got %d vs %d", results[0], results[1])
	}
}
