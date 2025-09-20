package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type SensorMessage struct {
	Topic     string                 `json:"topic"`
	Payload   map[string]interface{} `json:"payload"`
	Timestamp time.Time              `json:"timestamp"`
	ID        string                 `json:"id"`
	Retries   int                    `json:"retries"`
}

type Buffer struct {
	messages    []SensorMessage
	mutex       sync.RWMutex
	maxSize     int
	persistFile string
	apiURL      string
	apiKey      string
	httpClient  *http.Client

	// Resilience features
	circuitBreaker *CircuitBreaker
	backoffState   map[string]*BackoffState
	lastFlush      time.Time
	maxRetries     int
}

type CircuitBreaker struct {
	maxFailures  int
	timeout      time.Duration
	failures     int
	lastFailTime time.Time
	state        string // "closed", "open", "half-open"
	mutex        sync.RWMutex
}

type BackoffState struct {
	attempts    int
	nextAttempt time.Time
	maxDelay    time.Duration
}

// NewBuffer creates a new persistent buffer
func NewBuffer(maxSize int, persistFile string, apiURL string, apiKey string) *Buffer {
	buffer := &Buffer{
		messages:     make([]SensorMessage, 0),
		maxSize:      maxSize,
		persistFile:  persistFile,
		apiURL:       apiURL,
		apiKey:       apiKey,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		backoffState: make(map[string]*BackoffState),
		maxRetries:   5,
		circuitBreaker: &CircuitBreaker{
			maxFailures: 5,
			timeout:     30 * time.Second,
			state:       "closed",
		},
	}

	// Load existing messages from disk
	buffer.loadFromDisk()
	return buffer
}

// Add message to buffer with persistence
func (b *Buffer) Add(message SensorMessage) error {
	// Generate unique ID for message
	message.ID = fmt.Sprintf("%d-%s", time.Now().UnixNano(), message.Topic)
	message.Retries = 0

	// Critical section - add to buffer
	b.mutex.Lock()
	// Add to buffer
	b.messages = append(b.messages, message)

	// Rotate buffer if too large
	if len(b.messages) > b.maxSize {
		b.messages = b.messages[len(b.messages)-b.maxSize:]
	}

	// Create a copy for persistence to minimize lock time
	messagesCopy := make([]SensorMessage, len(b.messages))
	copy(messagesCopy, b.messages)
	b.mutex.Unlock()

	// Persist to disk outside of lock
	return b.saveToDiskWithData(messagesCopy)
}

// Get messages ready for sending
func (b *Buffer) GetPendingMessages() []SensorMessage {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	var pending []SensorMessage
	now := time.Now()

	for _, msg := range b.messages {
		// Check if message is ready to be sent based on backoff
		if backoff, exists := b.backoffState[msg.ID]; exists {
			if now.Before(backoff.nextAttempt) {
				continue // Skip this message, still in backoff
			}
		}
		pending = append(pending, msg)
	}

	return pending
}

// Send messages to API with resilience
func (b *Buffer) FlushToAPI() error {
	// Check circuit breaker
	if !b.circuitBreaker.CanAttempt() {
		return fmt.Errorf("circuit breaker is open")
	}

	messages := b.GetPendingMessages()
	if len(messages) == 0 {
		return nil
	}

	// Prepare payload
	payloadJSON, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("failed to marshal messages: %w", err)
	}

	log.Printf("Sending batch of %d messages", len(messages))

	// Create request
	req, err := http.NewRequest("POST", b.apiURL, bytes.NewBuffer(payloadJSON))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	req.Header.Set("apikey", b.apiKey)

	// Send request
	resp, err := b.httpClient.Do(req)
	if err != nil {
		b.circuitBreaker.RecordFailure()
		b.handleSendFailure(messages, err)
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for logging
	body, _ := io.ReadAll(resp.Body)

	// Handle response based on status code
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// Success - remove messages from buffer
		log.Printf("Successfully sent %d messages", len(messages))
		b.circuitBreaker.RecordSuccess()
		return b.removeMessages(messages)

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Client error - don't retry, remove messages
		log.Printf("Client error %d: %s", resp.StatusCode, string(body))
		return b.removeMessages(messages)

	case resp.StatusCode >= 500:
		// Server error - retry with backoff
		log.Printf("Server error %d: %s", resp.StatusCode, string(body))
		b.circuitBreaker.RecordFailure()
		return b.handleSendFailure(messages, fmt.Errorf("server error: %d", resp.StatusCode))

	default:
		log.Printf("Unexpected status code %d: %s", resp.StatusCode, string(body))
		return b.handleSendFailure(messages, fmt.Errorf("unexpected status: %d", resp.StatusCode))
	}
}

// Handle send failure with backoff and retry logic
func (b *Buffer) handleSendFailure(messages []SensorMessage, err error) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for _, msg := range messages {
		msg.Retries++

		// Update message in buffer
		for j, bufMsg := range b.messages {
			if bufMsg.ID == msg.ID {
				b.messages[j].Retries = msg.Retries
				break
			}
		}

		// Remove message if max retries reached
		if msg.Retries >= b.maxRetries {
			log.Printf("Message %s exceeded max retries, removing", msg.ID)
			b.removeMessageByID(msg.ID)
			continue
		}

		// Calculate backoff delay
		delay := time.Duration(1<<uint(msg.Retries)) * time.Second
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}

		// Set backoff state
		b.backoffState[msg.ID] = &BackoffState{
			attempts:    msg.Retries,
			nextAttempt: time.Now().Add(delay),
			maxDelay:    5 * time.Minute,
		}

		log.Printf("Message %s failed (attempt %d), retrying in %v", msg.ID, msg.Retries, delay)
	}

	return b.saveToDisk()
}

// Remove successfully sent messages from buffer
func (b *Buffer) removeMessages(messages []SensorMessage) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	messageIDs := make(map[string]bool)
	for _, msg := range messages {
		messageIDs[msg.ID] = true
		delete(b.backoffState, msg.ID) // Remove backoff state
	}

	// Filter out sent messages
	var remaining []SensorMessage
	for _, msg := range b.messages {
		if !messageIDs[msg.ID] {
			remaining = append(remaining, msg)
		}
	}

	b.messages = remaining
	b.lastFlush = time.Now()

	return b.saveToDisk()
}

// Remove message by ID
func (b *Buffer) removeMessageByID(id string) {
	for i, msg := range b.messages {
		if msg.ID == id {
			b.messages = append(b.messages[:i], b.messages[i+1:]...)
			delete(b.backoffState, id)
			break
		}
	}
}

// Save buffer to disk for persistence
func (b *Buffer) saveToDisk() error {
	if b.persistFile == "" {
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(b.persistFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write to temporary file first
	tempFile := b.persistFile + ".tmp"
	data, err := json.Marshal(b.messages)
	if err != nil {
		return fmt.Errorf("failed to marshal buffer: %w", err)
	}

	if err := os.WriteFile(tempFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempFile, b.persistFile); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// Save specific data to disk for persistence (used when we have a copy of messages)
func (b *Buffer) saveToDiskWithData(messages []SensorMessage) error {
	if b.persistFile == "" {
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(b.persistFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write to temporary file first
	tempFile := b.persistFile + ".tmp"
	data, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("failed to marshal buffer: %w", err)
	}

	if err := os.WriteFile(tempFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempFile, b.persistFile); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// Load buffer from disk
func (b *Buffer) loadFromDisk() error {
	if b.persistFile == "" {
		return nil
	}

	data, err := os.ReadFile(b.persistFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("No existing buffer file found, starting fresh")
			return nil
		}
		return fmt.Errorf("failed to read buffer file: %w", err)
	}

	if err := json.Unmarshal(data, &b.messages); err != nil {
		log.Printf("Failed to unmarshal buffer data: %v", err)
		// Start fresh if data is corrupted
		b.messages = make([]SensorMessage, 0)
		return nil
	}

	log.Printf("Loaded %d messages from disk", len(b.messages))
	return nil
}

// Get buffer statistics
func (b *Buffer) GetStats() map[string]interface{} {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	// Calculate pending messages without calling GetPendingMessages() to avoid nested locking
	var pendingCount int
	now := time.Now()
	for _, msg := range b.messages {
		// Check if message is ready to be sent based on backoff
		if backoff, exists := b.backoffState[msg.ID]; exists {
			if now.Before(backoff.nextAttempt) {
				continue // Skip this message, still in backoff
			}
		}
		pendingCount++
	}

	return map[string]interface{}{
		"total_messages":   len(b.messages),
		"pending_messages": pendingCount,
		"last_flush":       b.lastFlush,
		"circuit_breaker":  b.circuitBreaker.state,
		"backoff_count":    len(b.backoffState),
	}
}

// Circuit breaker implementation
func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	now := time.Now()

	switch cb.state {
	case "closed":
		return true
	case "open":
		if now.After(cb.lastFailTime.Add(cb.timeout)) {
			cb.state = "half-open"
			return true
		}
		return false
	case "half-open":
		return true
	default:
		return true
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failures = 0
	cb.state = "closed"
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failures++
	cb.lastFailTime = time.Now()

	if cb.failures >= cb.maxFailures {
		cb.state = "open"
	}
}

var buffer *Buffer

// Configuration structure
type Config struct {
	MQTT struct {
		Broker               string `json:"broker"`
		ClientID             string `json:"client_id"`
		Username             string `json:"username"`
		Password             string `json:"password"`
		ReconnectInterval    int    `json:"reconnect_interval"`
		MaxReconnectInterval int    `json:"max_reconnect_interval"`
	} `json:"mqtt"`
	API struct {
		URL     string `json:"url"`
		Key     string `json:"key"`
		Timeout int    `json:"timeout"`
	} `json:"api"`
	Buffer struct {
		MaxSize              int    `json:"max_size"`
		PersistFile          string `json:"persist_file"`
		FlushInterval        int    `json:"flush_interval"`
		MaxRetries           int    `json:"max_retries"`
		CleanupInterval      int    `json:"cleanup_interval"`
		MessageRetentionDays int    `json:"message_retention_days"`
	} `json:"buffer"`
	CircuitBreaker struct {
		MaxFailures int `json:"max_failures"`
		Timeout     int `json:"timeout"`
	} `json:"circuit_breaker"`
	Topics  []string `json:"topics"`
	Logging struct {
		Level         string `json:"level"`
		StatsInterval int    `json:"stats_interval"`
	} `json:"logging"`
}

// Load configuration from file or environment
func loadConfig() (*Config, error) {
	config := &Config{}

	// Default configuration file path
	configPath := "config.json"

	// Check for environment override
	if envConfig := os.Getenv("MQTT_BUFFER_CONFIG"); envConfig != "" {
		configPath = envConfig
	}

	// Load configuration file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Override persist file with PiKVM PST path if available
	if pstPath := os.Getenv("KVMD_PST_DATA"); pstPath != "" {
		config.Buffer.PersistFile = filepath.Join(pstPath, "mqtt-buffer.json")
		log.Printf("Using PiKVM persistent storage: %s", config.Buffer.PersistFile)
	} else if pstPath := os.Getenv("MQTT_BUFFER_PST_PATH"); pstPath != "" {
		config.Buffer.PersistFile = filepath.Join(pstPath, "mqtt-buffer.json")
		log.Printf("Using custom PST path: %s", config.Buffer.PersistFile)
	}

	return config, nil
}

func main() {
	log.Println("Starting MQTT Buffer Service for PiKVM...")

	// Load configuration
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Configuration loaded. Buffer file: %s", config.Buffer.PersistFile)

	// Initialize persistent buffer
	buffer = NewBuffer(
		config.Buffer.MaxSize,
		config.Buffer.PersistFile,
		config.API.URL,
		config.API.Key,
	)

	// Configure circuit breaker
	buffer.circuitBreaker.maxFailures = config.CircuitBreaker.MaxFailures
	buffer.circuitBreaker.timeout = time.Duration(config.CircuitBreaker.Timeout) * time.Second
	buffer.maxRetries = config.Buffer.MaxRetries

	log.Printf("Starting MQTT buffer service with %d existing messages", len(buffer.messages))

	// Configure MQTT client
	opts := mqtt.NewClientOptions().
		AddBroker(config.MQTT.Broker).
		SetClientID(config.MQTT.ClientID).
		SetUsername(config.MQTT.Username).
		SetPassword(config.MQTT.Password).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(time.Duration(config.MQTT.ReconnectInterval) * time.Second).
		SetMaxReconnectInterval(time.Duration(config.MQTT.MaxReconnectInterval) * time.Second)

	// Set connection lost handler
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		log.Printf("MQTT connection lost: %v", err)
	})

	// Set reconnect handler
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		log.Println("MQTT connected/reconnected")

		// Subscribe to configured topics
		for _, topic := range config.Topics {
			if topic == "tele/tasmota_F3E3A4/SENSOR" {
				// Special handler for Zigbee2Tasmota sensor data
				if token := client.Subscribe(topic, 0, handleSensorMessage); token.Wait() && token.Error() != nil {
					log.Printf("Failed to subscribe to sensor topic %s: %v", topic, token.Error())
				} else {
					log.Printf("Subscribed to sensor topic: %s", topic)
				}
			} else {
				// Generic handler for other topics
				if token := client.Subscribe(topic, 0, handleGenericMessage); token.Wait() && token.Error() != nil {
					log.Printf("Failed to subscribe to topic %s: %v", topic, token.Error())
				} else {
					log.Printf("Subscribed to topic: %s", topic)
				}
			}
		}
	})

	// Connect to MQTT broker
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Failed to connect to MQTT broker: %v", token.Error())
	}

	log.Println("Connected to MQTT broker")

	// Start buffer flush routine
	go bufferFlushRoutine(time.Duration(config.Buffer.FlushInterval) * time.Second)

	// Start statistics logging routine
	go statsRoutine(time.Duration(config.Logging.StatsInterval) * time.Second)

	// Start buffer cleanup routine
	go cleanupRoutine(time.Duration(config.Buffer.CleanupInterval)*time.Second,
		time.Duration(config.Buffer.MessageRetentionDays)*24*time.Hour)

	// Keep the program running
	select {}
}

// Handle sensor messages (Zigbee2Tasmota format)
func handleSensorMessage(client mqtt.Client, msg mqtt.Message) {
	var payload map[string]interface{}

	// Use the complete payload directly
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
		log.Printf("Failed to parse sensor message: %v", err)
		// If not JSON, store as raw payload
		payload = map[string]interface{}{
			"raw_payload": string(msg.Payload()),
		}
	}

	message := SensorMessage{
		Topic:     msg.Topic(),
		Payload:   payload,
		Timestamp: time.Now(),
	}

	if err := buffer.Add(message); err != nil {
		log.Printf("Failed to add message to buffer: %v", err)
	}
}

// Handle generic MQTT messages
func handleGenericMessage(client mqtt.Client, msg mqtt.Message) {
	var payload map[string]interface{}

	// Use the complete payload directly
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
		// If not JSON, store as raw payload
		payload = map[string]interface{}{
			"raw_payload": string(msg.Payload()),
		}
	}

	message := SensorMessage{
		Topic:     msg.Topic(),
		Payload:   payload,
		Timestamp: time.Now(),
	}

	if err := buffer.Add(message); err != nil {
		log.Printf("Failed to add generic message to buffer: %v", err)
	}
}

// Buffer flush routine - sends data to API
func bufferFlushRoutine(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		if err := buffer.FlushToAPI(); err != nil {
			log.Printf("Failed to flush buffer: %v", err)
		}
	}
}

// Statistics logging routine
func statsRoutine(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		stats := buffer.GetStats()
		log.Printf("Buffer stats: %+v", stats)
	}
}

// Cleanup routine - removes old messages and backoff states
func cleanupRoutine(cleanupInterval, retentionDuration time.Duration) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		buffer.mutex.Lock()

		// Remove old backoff states
		now := time.Now()
		for id, backoff := range buffer.backoffState {
			if now.After(backoff.nextAttempt.Add(24 * time.Hour)) {
				delete(buffer.backoffState, id)
			}
		}

		// Remove very old messages
		cutoff := now.Add(-retentionDuration)
		var kept []SensorMessage
		for _, msg := range buffer.messages {
			if msg.Timestamp.After(cutoff) {
				kept = append(kept, msg)
			}
		}

		if len(kept) < len(buffer.messages) {
			log.Printf("Cleaned up %d old messages", len(buffer.messages)-len(kept))
			buffer.messages = kept
			buffer.saveToDisk()
		}

		buffer.mutex.Unlock()
	}
}
