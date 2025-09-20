package main

import (
	"os"
	"testing"
	"time"
)

// TestNewBuffer tests the buffer initialization
func TestNewBuffer(t *testing.T) {
	buffer := NewBuffer(100, "/tmp/test-buffer.json", "http://api.test", "test-key")

	if buffer == nil {
		t.Fatal("NewBuffer returned nil")
	}

	if buffer.maxSize != 100 {
		t.Errorf("Expected maxSize 100, got %d", buffer.maxSize)
	}

	if len(buffer.messages) != 0 {
		t.Errorf("Expected empty buffer, got %d messages", len(buffer.messages))
	}

	// Cleanup
	os.Remove("/tmp/test-buffer.json")
}

// TestBuffer_Add tests adding messages to the buffer
func TestBuffer_Add(t *testing.T) {
	buffer := NewBuffer(5, "/tmp/test-add.json", "http://api.test", "test-key")
	defer os.Remove("/tmp/test-add.json")

	// Create a SensorMessage
	msg := SensorMessage{
		Topic:     "test/topic",
		Payload:   map[string]interface{}{"value": 42},
		Timestamp: time.Now(),
		ID:        "test-id-1",
		Retries:   0,
	}

	// Add the message
	err := buffer.Add(msg)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	if len(buffer.messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(buffer.messages))
	}

	storedMsg := buffer.messages[0]
	if storedMsg.Topic != "test/topic" {
		t.Errorf("Expected topic 'test/topic', got '%s'", storedMsg.Topic)
	}

	if storedMsg.Payload["value"] != 42 {
		t.Errorf("Expected payload value 42, got %v", storedMsg.Payload["value"])
	}
}

// TestBuffer_AddWithRotation tests buffer rotation when maxSize is exceeded
func TestBuffer_AddWithRotation(t *testing.T) {
	buffer := NewBuffer(2, "/tmp/test-rotation.json", "http://api.test", "test-key")
	defer os.Remove("/tmp/test-rotation.json")

	// Add messages beyond max size
	msg1 := SensorMessage{Topic: "topic1", Payload: map[string]interface{}{"value": 1}, Timestamp: time.Now(), ID: "id1"}
	msg2 := SensorMessage{Topic: "topic2", Payload: map[string]interface{}{"value": 2}, Timestamp: time.Now(), ID: "id2"}
	msg3 := SensorMessage{Topic: "topic3", Payload: map[string]interface{}{"value": 3}, Timestamp: time.Now(), ID: "id3"}

	buffer.Add(msg1)
	buffer.Add(msg2)
	buffer.Add(msg3)

	if len(buffer.messages) != 2 {
		t.Errorf("Expected 2 messages after rotation, got %d", len(buffer.messages))
	}

	// First message should be removed, second and third should remain
	if buffer.messages[0].Payload["value"] != 2 {
		t.Errorf("Expected first message value 2, got %v", buffer.messages[0].Payload["value"])
	}

	if buffer.messages[1].Payload["value"] != 3 {
		t.Errorf("Expected second message value 3, got %v", buffer.messages[1].Payload["value"])
	}
}

// TestBuffer_Persistence tests saving and loading buffer from disk
func TestBuffer_Persistence(t *testing.T) {
	testFile := "/tmp/test-persistence.json"
	defer os.Remove(testFile)

	// Create buffer and add messages
	buffer1 := NewBuffer(10, testFile, "http://api.test", "test-key")
	msg1 := SensorMessage{Topic: "topic1", Payload: map[string]interface{}{"value": 1}, Timestamp: time.Now(), ID: "id1"}
	msg2 := SensorMessage{Topic: "topic2", Payload: map[string]interface{}{"value": 2}, Timestamp: time.Now(), ID: "id2"}

	buffer1.Add(msg1)
	buffer1.Add(msg2)

	// Save to disk
	err := buffer1.saveToDisk()
	if err != nil {
		t.Fatalf("Failed to save to disk: %v", err)
	}

	// Create new buffer and load from disk
	buffer2 := NewBuffer(10, testFile, "http://api.test", "test-key")

	if len(buffer2.messages) != 2 {
		t.Errorf("Expected 2 messages after loading, got %d", len(buffer2.messages))
		return
	}

	if buffer2.messages[0].Payload["value"] != float64(1) {
		t.Errorf("Expected first message value 1, got %v", buffer2.messages[0].Payload["value"])
	}
}

// TestCircuitBreaker_BasicStates tests circuit breaker state transitions
func TestCircuitBreaker_BasicStates(t *testing.T) {
	cb := &CircuitBreaker{
		maxFailures: 3,
		timeout:     time.Second,
		failures:    0,
	}

	// Initially should be closed (allow requests)
	if !cb.CanAttempt() {
		t.Error("Circuit breaker should be closed initially")
	}

	// Add failures
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	// Should be open after max failures
	if cb.CanAttempt() {
		t.Error("Circuit breaker should be open after max failures")
	}

	// Reset should close it
	cb.RecordSuccess()
	if !cb.CanAttempt() {
		t.Error("Circuit breaker should be closed after reset")
	}
}

// TestBuffer_GetPendingMessages tests retrieving pending messages
func TestBuffer_GetPendingMessages(t *testing.T) {
	buffer := NewBuffer(10, "/tmp/test-pending.json", "http://api.test", "test-key")
	defer os.Remove("/tmp/test-pending.json")

	// Add messages
	msg1 := SensorMessage{Topic: "topic1", Payload: map[string]interface{}{"value": 1}, Timestamp: time.Now(), ID: "id1"}
	msg2 := SensorMessage{Topic: "topic2", Payload: map[string]interface{}{"value": 2}, Timestamp: time.Now(), ID: "id2"}

	buffer.Add(msg1)
	buffer.Add(msg2)

	pending := buffer.GetPendingMessages()

	if len(pending) != 2 {
		t.Errorf("Expected 2 pending messages, got %d", len(pending))
	}
}
