package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test formatSize function
func TestFormatSize(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1024 * 1024, "1.00 MB"},
		{1536 * 1024, "1.50 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
		{5 * 1024 * 1024 * 1024, "5.00 GB"},
	}

	for _, test := range tests {
		result := formatSize(test.input)
		if result != test.expected {
			t.Errorf("formatSize(%d) = %s, expected %s", test.input, result, test.expected)
		}
	}
}

// Test calculateDirSize
func TestCalculateDirSize(t *testing.T) {
	// Create temp directory
	tempDir, err := os.MkdirTemp("", "fileshare_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test empty directory
	size, err := calculateDirSize(tempDir)
	if err != nil {
		t.Errorf("calculateDirSize(empty dir) error: %v", err)
	}
	if size != 0 {
		t.Errorf("calculateDirSize(empty dir) = %d, expected 0", size)
	}

	// Create files
	content1 := []byte("Hello World")
	content2 := []byte(strings.Repeat("A", 1000))

	if err := os.WriteFile(filepath.Join(tempDir, "file1.txt"), content1, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	subDir := filepath.Join(tempDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), content2, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Calculate expected size
	expectedSize := int64(len(content1) + len(content2))

	size, err = calculateDirSize(tempDir)
	if err != nil {
		t.Errorf("calculateDirSize error: %v", err)
	}
	if size != expectedSize {
		t.Errorf("calculateDirSize = %d, expected %d", size, expectedSize)
	}
}

// Test getLocalIPs
func TestGetLocalIPs(t *testing.T) {
	ips := getLocalIPs()
	if len(ips) == 0 {
		t.Error("getLocalIPs returned empty list")
	}

	// Should always have localhost
	hasLocalhost := false
	for _, ip := range ips {
		if ip == "127.0.0.1" {
			hasLocalhost = true
			break
		}
	}
	if !hasLocalhost {
		t.Error("getLocalIPs should include 127.0.0.1")
	}

	// Verify all IPs are valid
	for _, ip := range ips {
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil {
			t.Errorf("Invalid IP returned: %s", ip)
		}
		if parsedIP.To4() == nil {
			t.Errorf("Non-IPv4 address returned: %s", ip)
		}
	}
}

// Test FileServer acquire/release client
func TestFileServerClientManagement(t *testing.T) {
	fs := NewFileServer("send", "/tmp", 8080, false)

	// Test acquire first client
	if !fs.acquireClient("192.168.1.1") {
		t.Error("First acquire should succeed")
	}

	// Test same client can acquire again
	if !fs.acquireClient("192.168.1.1") {
		t.Error("Same client should be able to acquire again")
	}

	// Test different client cannot acquire
	if fs.acquireClient("192.168.1.2") {
		t.Error("Different client should not be able to acquire when active")
	}

	// Test release and re-acquire
	fs.releaseClient("192.168.1.1")

	if !fs.acquireClient("192.168.1.2") {
		t.Error("New client should acquire after release")
	}
}

// Test getClientIP
func TestGetClientIP(t *testing.T) {
	fs := NewFileServer("send", "/tmp", 8080, false)

	tests := []struct {
		remoteAddr string
		expected   string
	}{
		{"192.168.1.1:12345", "192.168.1.1"},
		{"[::1]:12345", "::1"},
		{"10.0.0.1:8080", "10.0.0.1"},
		{"127.0.0.1:3000", "127.0.0.1"},
	}

	for _, test := range tests {
		// Create a real HTTP request with RemoteAddr set
		req, _ := http.NewRequest("GET", "/test", nil)
		req.RemoteAddr = test.remoteAddr
		result := fs.getClientIP(req)
		if result != test.expected {
			t.Errorf("getClientIP(%s) = %s, expected %s", test.remoteAddr, result, test.expected)
		}
	}
}

// Test TransferStatus updates
func TestTransferStatus(t *testing.T) {
	fs := NewFileServer("send", "/tmp/test.txt", 8080, false)

	// Test initial status
	fs.statusMu.RLock()
	if fs.status.Status != "waiting" {
		t.Errorf("Initial status should be 'waiting', got %s", fs.status.Status)
	}
	if fs.status.Mode != "send" {
		t.Errorf("Mode should be 'send', got %s", fs.status.Mode)
	}
	fs.statusMu.RUnlock()

	// Test status update
	fs.statusMu.Lock()
	fs.status.Status = "transferring"
	fs.status.Transferred = 500
	fs.status.Size = 1000
	fs.status.Progress = 50.0
	fs.statusMu.Unlock()

	fs.statusMu.RLock()
	if fs.status.Status != "transferring" {
		t.Errorf("Status should be 'transferring', got %s", fs.status.Status)
	}
	if fs.status.Progress != 50.0 {
		t.Errorf("Progress should be 50.0, got %f", fs.status.Progress)
	}
	fs.statusMu.RUnlock()
}

// Test log functionality
func TestAddLog(t *testing.T) {
	fs := NewFileServer("send", "/tmp", 8080, false)

	fs.addLog("Test message 1")
	fs.addLog("Test message 2")
	fs.addLog("Test message 3")

	fs.logMu.RLock()
	if len(fs.transferLog) != 3 {
		t.Errorf("Expected 3 log entries, got %d", len(fs.transferLog))
	}

	// Check log format
	for i, log := range fs.transferLog {
		if !strings.Contains(log, "Test message") {
			t.Errorf("Log entry %d doesn't contain expected content: %s", i, log)
		}
		// Should have timestamp format [HH:MM:SS]
		if !strings.HasPrefix(log, "[") {
			t.Errorf("Log entry %d should start with timestamp: %s", i, log)
		}
	}
	fs.logMu.RUnlock()

	// Test log limit (100 entries)
	for i := 0; i < 105; i++ {
		fs.addLog(fmt.Sprintf("Message %d", i))
	}

	fs.logMu.RLock()
	if len(fs.transferLog) > 100 {
		t.Errorf("Log should be limited to 100 entries, got %d", len(fs.transferLog))
	}
	fs.logMu.RUnlock()
}

// Test path validation in main function logic
func TestPathValidation(t *testing.T) {
	// This test would require refactoring main() to be testable
	// For now, we document the expected behavior

	tests := []struct {
		mode     string
		path     string
		shouldOK bool
	}{
		{"send", "/nonexistent", false},
		{"send", "/tmp", true},
		{"recv", "/tmp", true},
		{"invalid", "/tmp", false},
	}

	for _, test := range tests {
		// Note: This is a conceptual test
		// Actual validation is done in main()
		t.Logf("Mode: %s, Path: %s, ShouldOK: %v", test.mode, test.path, test.shouldOK)
	}
}

// Test progress calculation
func TestProgressCalculation(t *testing.T) {
	fs := NewFileServer("send", "/tmp/test.txt", 8080, false)
	fs.status.Size = 1000

	testCases := []struct {
		transferred int64
		expected    float64
	}{
		{0, 0.0},
		{250, 25.0},
		{500, 50.0},
		{750, 75.0},
		{1000, 100.0},
	}

	for _, tc := range testCases {
		progress := float64(tc.transferred) / float64(fs.status.Size) * 100
		if progress != tc.expected {
			t.Errorf("Progress calculation: %d/1000 = %f, expected %f",
				tc.transferred, progress, tc.expected)
		}
	}
}

// Test SSE client management
func TestSSEClientManagement(t *testing.T) {
	fs := NewFileServer("send", "/tmp", 8080, false)

	// Create test channels
	ch1 := make(chan string, 10)
	ch2 := make(chan string, 10)

	// Add clients
	fs.sseMu.Lock()
	fs.sseClients[ch1] = true
	fs.sseClients[ch2] = true
	fs.sseMu.Unlock()

	// Verify clients added
	fs.sseMu.RLock()
	if len(fs.sseClients) != 2 {
		t.Errorf("Expected 2 SSE clients, got %d", len(fs.sseClients))
	}
	fs.sseMu.RUnlock()

	// Remove one client
	fs.sseMu.Lock()
	delete(fs.sseClients, ch1)
	fs.sseMu.Unlock()

	fs.sseMu.RLock()
	if len(fs.sseClients) != 1 {
		t.Errorf("Expected 1 SSE client after removal, got %d", len(fs.sseClients))
	}
	fs.sseMu.RUnlock()

	close(ch1)
	close(ch2)
}

// Benchmark for calculateDirSize
func BenchmarkCalculateDirSize(b *testing.B) {
	// Create test directory with files
	tempDir, _ := os.MkdirTemp("", "bench_*")
	defer os.RemoveAll(tempDir)

	// Create 100 files with 1KB each
	for i := 0; i < 100; i++ {
		content := make([]byte, 1024)
		os.WriteFile(filepath.Join(tempDir, fmt.Sprintf("file%d.txt", i)), content, 0644)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		calculateDirSize(tempDir)
	}
}

// Test concurrent status updates
func TestConcurrentStatusUpdates(t *testing.T) {
	fs := NewFileServer("send", "/tmp", 8080, false)
	fs.status.Size = 10000

	// Simulate concurrent updates
	done := make(chan bool, 3)

	go func() {
		for i := 0; i < 100; i++ {
			fs.statusMu.Lock()
			fs.status.Transferred += 10
			fs.status.Progress = float64(fs.status.Transferred) / float64(fs.status.Size) * 100
			fs.statusMu.Unlock()
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			fs.statusMu.RLock()
			_ = fs.status.Status
			fs.statusMu.RUnlock()
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 50; i++ {
			fs.broadcastStatus()
			time.Sleep(time.Microsecond * 10)
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 3; i++ {
		<-done
	}

	// Verify final state
	fs.statusMu.RLock()
	if fs.status.Transferred != 1000 {
		t.Errorf("Expected transferred = 1000, got %d", fs.status.Transferred)
	}
	fs.statusMu.RUnlock()
}
