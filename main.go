package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultPort = 0

type TransferStatus struct {
	Mode           string    `json:"mode"`
	Path           string    `json:"path"`
	Size           int64     `json:"size"`
	Transferred    int64     `json:"transferred"`
	Progress       float64   `json:"progress"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	ClientIP       string    `json:"client_ip,omitempty"`
	StartTime      time.Time `json:"start_time"`
	LastUpdateTime time.Time `json:"last_update_time"`
}

type FileServer struct {
	mode         string
	path         string
	port         int
	status       *TransferStatus
	statusMu     sync.RWMutex
	sseClients   map[chan string]bool
	sseMu        sync.RWMutex
	autoExit     bool
	server       *http.Server
	activeClient string
	activeMu     sync.Mutex
	transferLog  []string
	logMu        sync.RWMutex
}

var (
	mode     string
	path     string
	autoExit bool
	port     int
	server   *FileServer
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <send|recv> <path>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  send <path>     Send file or directory\n")
		fmt.Fprintf(os.Stderr, "  recv <dir>      Receive files to directory\n")
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
	}

	flag.IntVar(&port, "p", DefaultPort, "Port to listen on (0 for random)")
	flag.BoolVar(&autoExit, "auto-exit", false, "Auto exit after transfer complete")
	flag.Parse()

	args := flag.Args()
	if len(args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	mode = args[0]
	path = args[1]

	if mode != "send" && mode != "recv" {
		fmt.Fprintf(os.Stderr, "Error: mode must be 'send' or 'recv'\n")
		flag.Usage()
		os.Exit(1)
	}

	if mode == "send" {
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot access '%s': %v\n", path, err)
			os.Exit(1)
		}
	} else {
		if err := os.MkdirAll(path, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot create directory '%s': %v\n", path, err)
			os.Exit(1)
		}
	}

	server = NewFileServer(mode, path, port, autoExit)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewFileServer(mode, path string, port int, autoExit bool) *FileServer {
	return &FileServer{
		mode:       mode,
		path:       path,
		port:       port,
		autoExit:   autoExit,
		sseClients: make(map[chan string]bool),
		transferLog: make([]string, 0),
		status: &TransferStatus{
			Mode:      mode,
			Path:      filepath.Base(path),
			Status:    "waiting",
			StartTime: time.Now(),
		},
	}
}

func (fs *FileServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", fs.handleIndex)
	mux.HandleFunc("/api/info", fs.handleInfo)
	mux.HandleFunc("/api/events", fs.handleEvents)
	mux.HandleFunc("/api/download", fs.handleDownload)
	mux.HandleFunc("/api/upload", fs.handleUpload)
	mux.HandleFunc("/api/cancel", fs.handleCancel)
	mux.HandleFunc("/api/log", fs.handleLog)

	fs.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", fs.port),
		Handler: mux,
	}

	listener, err := net.Listen("tcp", fs.server.Addr)
	if err != nil {
		return err
	}
	fs.port = listener.Addr().(*net.TCPAddr).Port

	fs.statusMu.Lock()
	fs.status.LastUpdateTime = time.Now()
	fs.statusMu.Unlock()

	fs.printInfo()

	go func() {
		if err := fs.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		}
	}()

	if fs.autoExit {
		fs.waitForComplete()
	} else {
		select {}
	}

	return nil
}

func (fs *FileServer) printInfo() {
	fmt.Println("╔════════════════════════════════════╗")
	fmt.Println("║        FileShare - Ready           ║")
	fmt.Println("╚════════════════════════════════════╝")
	fmt.Printf("\n📤 Mode: %s\n", strings.ToUpper(fs.mode))

	info, err := os.Stat(fs.path)
	if err == nil {
		if info.IsDir() {
			size, _ := calculateDirSize(fs.path)
			fmt.Printf("📁 Target: %s (directory, %s)\n", filepath.Base(fs.path), formatSize(size))
		} else {
			fmt.Printf("📄 Target: %s (%s)\n", filepath.Base(fs.path), formatSize(info.Size()))
		}
	}

	fmt.Printf("\n🔗 URLs:\n")
	ips := getLocalIPs()
	for _, ip := range ips {
		fmt.Printf("   http://%s:%d\n", ip, fs.port)
	}

	if fs.autoExit {
		fmt.Println("\n⚡ Auto-exit enabled")
	}
	fmt.Println("\n⏹️  Press Ctrl+C to stop")
	fmt.Println()
}

func (fs *FileServer) addLog(message string) {
	fs.logMu.Lock()
	timestamp := time.Now().Format("15:04:05")
	logEntry := fmt.Sprintf("[%s] %s", timestamp, message)
	fs.transferLog = append(fs.transferLog, logEntry)
	if len(fs.transferLog) > 100 {
		fs.transferLog = fs.transferLog[len(fs.transferLog)-100:]
	}
	fs.logMu.Unlock()
	fs.broadcastStatus()
}

func (fs *FileServer) getClientIP(r *http.Request) string {
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return strings.Trim(ip, "[]")
}

func (fs *FileServer) acquireClient(clientIP string) bool {
	fs.activeMu.Lock()
	defer fs.activeMu.Unlock()

	if fs.activeClient != "" && fs.activeClient != clientIP {
		return false
	}
	if fs.activeClient == "" {
		fs.activeClient = clientIP
	}
	return true
}

func (fs *FileServer) releaseClient(clientIP string) {
	shouldLog := false
	fs.activeMu.Lock()
	if fs.activeClient == clientIP {
		fs.activeClient = ""
		shouldLog = true
	}
	fs.activeMu.Unlock()
	if shouldLog {
		fs.addLog(fmt.Sprintf("Client %s disconnected", clientIP))
	}
}

func (fs *FileServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

func (fs *FileServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	fs.statusMu.RLock()
	status := *fs.status
	fs.statusMu.RUnlock()

	fs.activeMu.Lock()
	activeClient := fs.activeClient
	fs.activeMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"mode":"%s","path":"%s","size":%d,"transferred":%d,"progress":%.2f,"status":"%s","error":"%s","client_ip":"%s"}`,
		status.Mode, status.Path, status.Size, status.Transferred, status.Progress, status.Status, status.Error, activeClient)
}

func (fs *FileServer) handleLog(w http.ResponseWriter, r *http.Request) {
	fs.logMu.RLock()
	logs := make([]string, len(fs.transferLog))
	copy(logs, fs.transferLog)
	fs.logMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `[`)
	for i, log := range logs {
		if i > 0 {
			fmt.Fprintf(w, `,`)
		}
		fmt.Fprintf(w, `"%s"`, log)
	}
	fmt.Fprintf(w, `]`)
}

func (fs *FileServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	clientChan := make(chan string, 10)
	fs.sseMu.Lock()
	fs.sseClients[clientChan] = true
	fs.sseMu.Unlock()

	defer func() {
		fs.sseMu.Lock()
		delete(fs.sseClients, clientChan)
		fs.sseMu.Unlock()
		close(clientChan)
	}()

	fs.statusMu.RLock()
	status := *fs.status
	fs.statusMu.RUnlock()

	fs.activeMu.Lock()
	activeClient := fs.activeClient
	fs.activeMu.Unlock()

	data := fmt.Sprintf(`{"status":"%s","progress":%.2f,"transferred":%d,"client_ip":"%s"}`,
		status.Status, status.Progress, status.Transferred, activeClient)
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case data, ok := <-clientChan:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ":heartbeat\n\n")
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (fs *FileServer) broadcastStatus() {
	fs.statusMu.RLock()
	status := *fs.status
	fs.statusMu.RUnlock()

	fs.activeMu.Lock()
	activeClient := fs.activeClient
	fs.activeMu.Unlock()

	data := fmt.Sprintf(`{"status":"%s","progress":%.2f,"transferred":%d,"client_ip":"%s","error":"%s"}`,
		status.Status, status.Progress, status.Transferred, activeClient, status.Error)

	fs.sseMu.RLock()
	defer fs.sseMu.RUnlock()
	for client := range fs.sseClients {
		select {
		case client <- data:
		default:
		}
	}
}

func (fs *FileServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	if fs.mode != "send" {
		http.Error(w, "Server is not in send mode", http.StatusBadRequest)
		return
	}

	clientIP := fs.getClientIP(r)

	if !fs.acquireClient(clientIP) {
		http.Error(w, "Another client is already connected", http.StatusServiceUnavailable)
		return
	}
	fs.addLog(fmt.Sprintf("Client %s connected", clientIP))
	defer fs.releaseClient(clientIP)

	info, err := os.Stat(fs.path)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	fs.statusMu.Lock()
	fs.status.Status = "transferring"
	fs.status.ClientIP = clientIP
	if info.IsDir() {
		fs.status.Size, _ = calculateDirSize(fs.path)
	} else {
		fs.status.Size = info.Size()
	}
	fs.statusMu.Unlock()
	fs.broadcastStatus()
	fs.addLog(fmt.Sprintf("Started download from %s", clientIP))

	if info.IsDir() {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.zip\"", filepath.Base(fs.path)))

		zipWriter := zip.NewWriter(w)
		defer zipWriter.Close()

		var transferred int64
		basePath := fs.path

		filepath.Walk(basePath, func(file string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			relPath, _ := filepath.Rel(basePath, file)
			if relPath == "." {
				return nil
			}

			header, _ := zip.FileInfoHeader(fi)
			header.Name = relPath
			if fi.IsDir() {
				header.Name += "/"
			}

			writer, _ := zipWriter.CreateHeader(header)
			if !fi.IsDir() {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				n, _ := io.Copy(writer, f)
				f.Close()
				transferred += n

				fs.statusMu.Lock()
				fs.status.Transferred = transferred
				if fs.status.Size > 0 {
					fs.status.Progress = float64(transferred) / float64(fs.status.Size) * 100
				}
				fs.status.LastUpdateTime = time.Now()
				fs.statusMu.Unlock()
				fs.broadcastStatus()
			}
			return nil
		})
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(fs.path)))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))

		if r.Header.Get("Range") != "" {
			http.ServeContent(w, r, filepath.Base(fs.path), info.ModTime(), mustOpen(fs.path))
		} else {
			f, err := os.Open(fs.path)
			if err != nil {
				http.Error(w, "Failed to open file", http.StatusInternalServerError)
				return
			}
			defer f.Close()

			var transferred int64
			buf := make([]byte, 64*1024)
			for {
				n, err := f.Read(buf)
				if n > 0 {
					_, writeErr := w.Write(buf[:n])
					if writeErr != nil {
						// Client disconnected or write error
						fs.statusMu.Lock()
						fs.status.Status = "error"
						fs.status.Error = writeErr.Error()
						fs.statusMu.Unlock()
						fs.broadcastStatus()
						return
					}
					transferred += int64(n)

					fs.statusMu.Lock()
					fs.status.Transferred = transferred
					if fs.status.Size > 0 {
						fs.status.Progress = float64(transferred) / float64(fs.status.Size) * 100
					}
					fs.status.LastUpdateTime = time.Now()
					fs.statusMu.Unlock()
					fs.broadcastStatus()
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					fs.statusMu.Lock()
					fs.status.Status = "error"
					fs.status.Error = err.Error()
					fs.statusMu.Unlock()
					fs.broadcastStatus()
					return
				}
			}
		}
	}

	fs.statusMu.Lock()
	fs.status.Status = "completed"
	fs.status.Progress = 100
	fs.statusMu.Unlock()
	fs.broadcastStatus()
	fs.addLog(fmt.Sprintf("Download completed for %s", clientIP))

	fmt.Printf("\n✓ Transfer completed to %s\n", clientIP)
}

func (fs *FileServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	if fs.mode != "recv" {
		http.Error(w, "Server is not in receive mode", http.StatusBadRequest)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := fs.getClientIP(r)

	if !fs.acquireClient(clientIP) {
		http.Error(w, "Another client is already connected", http.StatusServiceUnavailable)
		return
	}
	defer fs.releaseClient(clientIP)

	r.ParseMultipartForm(10 << 30)

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	savePath := filepath.Join(fs.path, header.Filename)
	if _, err := os.Stat(savePath); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"error":"file_exists","message":"File '%s' already exists","path":"%s"}`,
			header.Filename, savePath)
		return
	}

	fs.statusMu.Lock()
	fs.status.Status = "transferring"
	fs.status.ClientIP = clientIP
	fs.status.Size = header.Size
	fs.statusMu.Unlock()
	fs.broadcastStatus()
	fs.addLog(fmt.Sprintf("Started upload from %s: %s", clientIP, header.Filename))

	dst, err := os.Create(savePath)
	if err != nil {
		fs.statusMu.Lock()
		fs.status.Status = "error"
		fs.status.Error = err.Error()
		fs.statusMu.Unlock()
		fs.broadcastStatus()
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	var transferred int64
	buf := make([]byte, 64*1024)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			dst.Write(buf[:n])
			transferred += int64(n)

			fs.statusMu.Lock()
			fs.status.Transferred = transferred
			if fs.status.Size > 0 {
				fs.status.Progress = float64(transferred) / float64(fs.status.Size) * 100
			}
			fs.status.LastUpdateTime = time.Now()
			fs.statusMu.Unlock()
			fs.broadcastStatus()
		}
		if err != nil {
			break
		}
	}

	fs.statusMu.Lock()
	fs.status.Status = "completed"
	fs.status.Progress = 100
	fs.statusMu.Unlock()
	fs.broadcastStatus()
	fs.addLog(fmt.Sprintf("Upload completed from %s: %s (%s)", clientIP, header.Filename, formatSize(transferred)))

	fmt.Printf("\n✓ Received '%s' from %s (%s)\n", header.Filename, clientIP, formatSize(transferred))

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"success","path":"%s","size":%d}`, savePath, transferred)
}

func (fs *FileServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := fs.getClientIP(r)
	fs.releaseClient(clientIP)

	fs.statusMu.Lock()
	fs.status.Status = "cancelled"
	fs.statusMu.Unlock()
	fs.broadcastStatus()
	fs.addLog(fmt.Sprintf("Transfer cancelled by %s", clientIP))

	fmt.Println("\n✗ Transfer cancelled")

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"cancelled"}`)
}

func (fs *FileServer) waitForComplete() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		fs.statusMu.RLock()
		status := fs.status.Status
		fs.statusMu.RUnlock()

		if status == "completed" || status == "cancelled" || status == "error" {
			time.Sleep(500 * time.Millisecond)
			fs.server.Shutdown(nil)
			os.Exit(0)
		}
	}
}

func getLocalIPs() []string {
	var ips []string
	ips = append(ips, "127.0.0.1")

	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				if ipnet.IP.To4() != nil {
					ips = append(ips, ipnet.IP.String())
				}
			}
		}
	}

	return ips
}

func calculateDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func formatSize(size int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", float64(size)/float64(GB))
	case size >= MB:
		return fmt.Sprintf("%.2f MB", float64(size)/float64(MB))
	case size >= KB:
		return fmt.Sprintf("%.2f KB", float64(size)/float64(KB))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func mustOpen(path string) *os.File {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	return f
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>FileShare</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }
        .container {
            background: white;
            border-radius: 16px;
            box-shadow: 0 20px 60px rgba(0,0,0,0.3);
            padding: 40px;
            max-width: 500px;
            width: 100%;
        }
        h1 {
            text-align: center;
            color: #333;
            margin-bottom: 8px;
            font-size: 28px;
        }
        .subtitle {
            text-align: center;
            color: #666;
            margin-bottom: 30px;
            font-size: 14px;
        }
        .info-box {
            background: #f5f5f5;
            border-radius: 8px;
            padding: 15px;
            margin-bottom: 20px;
            font-size: 13px;
        }
        .info-box .label {
            color: #666;
            font-weight: 600;
            margin-bottom: 4px;
        }
        .info-box .value {
            color: #333;
            word-break: break-all;
        }
        .drop-zone {
            border: 3px dashed #ddd;
            border-radius: 12px;
            padding: 40px 20px;
            text-align: center;
            cursor: pointer;
            transition: all 0.3s;
            margin-bottom: 20px;
        }
        .drop-zone:hover, .drop-zone.dragover {
            border-color: #667eea;
            background: #f8f9ff;
        }
        .drop-zone .icon {
            font-size: 48px;
            margin-bottom: 10px;
        }
        .drop-zone .text {
            color: #666;
            font-size: 14px;
        }
        .progress-container {
            display: none;
            margin-bottom: 20px;
        }
        .progress-container.active {
            display: block;
        }
        .progress-bar {
            height: 8px;
            background: #eee;
            border-radius: 4px;
            overflow: hidden;
            margin-bottom: 10px;
        }
        .progress-fill {
            height: 100%;
            background: linear-gradient(90deg, #667eea, #764ba2);
            width: 0%;
            transition: width 0.3s;
        }
        .progress-text {
            text-align: center;
            font-size: 14px;
            color: #666;
        }
        .status {
            text-align: center;
            padding: 10px;
            border-radius: 8px;
            margin-bottom: 15px;
            font-size: 14px;
            font-weight: 500;
        }
        .status.waiting {
            background: #fff3cd;
            color: #856404;
        }
        .status.transferring {
            background: #d1ecf1;
            color: #0c5460;
        }
        .status.completed {
            background: #d4edda;
            color: #155724;
        }
        .status.cancelled {
            background: #f8d7da;
            color: #721c24;
        }
        .status.error {
            background: #f8d7da;
            color: #721c24;
        }
        .log-container {
            background: #1e1e1e;
            border-radius: 8px;
            padding: 15px;
            margin-top: 20px;
            max-height: 200px;
            overflow-y: auto;
        }
        .log-title {
            color: #fff;
            font-size: 12px;
            font-weight: 600;
            margin-bottom: 10px;
            text-transform: uppercase;
            letter-spacing: 1px;
        }
        .log-entry {
            color: #aaa;
            font-size: 12px;
            font-family: 'Courier New', monospace;
            margin-bottom: 4px;
            line-height: 1.4;
        }
        .log-entry:last-child {
            color: #fff;
        }
        .btn {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            border: none;
            padding: 12px 30px;
            border-radius: 8px;
            cursor: pointer;
            font-size: 14px;
            font-weight: 600;
            width: 100%;
            transition: transform 0.2s, box-shadow 0.2s;
        }
        .btn:hover {
            transform: translateY(-2px);
            box-shadow: 0 5px 20px rgba(102, 126, 234, 0.4);
        }
        .btn:disabled {
            background: #ccc;
            cursor: not-allowed;
            transform: none;
            box-shadow: none;
        }
        .btn-cancel {
            background: #dc3545;
            margin-top: 10px;
        }
        .btn-cancel:hover {
            box-shadow: 0 5px 20px rgba(220, 53, 69, 0.4);
        }
        .hidden {
            display: none !important;
        }
        .curl-help {
            background: #f8f9fa;
            border-left: 4px solid #667eea;
            padding: 15px;
            margin-top: 20px;
            border-radius: 0 8px 8px 0;
        }
        .curl-help h3 {
            font-size: 14px;
            margin-bottom: 10px;
            color: #333;
        }
        .curl-help code {
            display: block;
            background: #2d2d2d;
            color: #f8f8f2;
            padding: 10px;
            border-radius: 4px;
            font-size: 12px;
            font-family: 'Courier New', monospace;
            margin-bottom: 8px;
            overflow-x: auto;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>📤 FileShare</h1>
        <p class="subtitle">LAN File Transfer Tool</p>
        
        <div class="info-box">
            <div class="label">Mode</div>
            <div class="value" id="mode">-</div>
        </div>
        
        <div class="info-box">
            <div class="label">Target</div>
            <div class="value" id="target">-</div>
        </div>
        
        <div class="info-box">
            <div class="label">Connected Client</div>
            <div class="value" id="client-ip">-</div>
        </div>
        
        <div class="status waiting" id="status">Waiting for connection...</div>
        
        <div id="upload-section">
            <div class="drop-zone" id="drop-zone">
                <div class="icon">📁</div>
                <div class="text">Drop files here or click to select</div>
                <input type="file" id="file-input" style="display: none;">
            </div>
        </div>
        
        <div id="download-section" class="hidden">
            <button class="btn" id="download-btn">Download File</button>
        </div>
        
        <div class="progress-container" id="progress">
            <div class="progress-bar">
                <div class="progress-fill" id="progress-fill"></div>
            </div>
            <div class="progress-text" id="progress-text">0%</div>
        </div>
        
        <button class="btn btn-cancel hidden" id="cancel-btn">Cancel Transfer</button>
        
        <div class="log-container">
            <div class="log-title">Transfer Log</div>
            <div id="log-entries"></div>
        </div>
        
        <div class="curl-help">
            <h3>🖥️ Command Line (curl)</h3>
            <code id="curl-cmd"># Loading...</code>
            <small style="color: #666;">Copy and run this in your terminal</small>
        </div>
    </div>

    <script>
        const dropZone = document.getElementById('drop-zone');
        const fileInput = document.getElementById('file-input');
        const progressContainer = document.getElementById('progress');
        const progressFill = document.getElementById('progress-fill');
        const progressText = document.getElementById('progress-text');
        const statusEl = document.getElementById('status');
        const cancelBtn = document.getElementById('cancel-btn');
        const uploadSection = document.getElementById('upload-section');
        const downloadSection = document.getElementById('download-section');
        const downloadBtn = document.getElementById('download-btn');
        const logEntries = document.getElementById('log-entries');
        const curlCmd = document.getElementById('curl-cmd');
        
        let currentMode = '';
        let eventSource = null;
        
        // Initialize
        async function init() {
            await updateInfo();
            connectSSE();
            fetchLogs();
        }
        
        async function updateInfo() {
            try {
                const response = await fetch('/api/info');
                const data = await response.json();
                currentMode = data.mode;
                
                document.getElementById('mode').textContent = data.mode.toUpperCase();
                document.getElementById('target').textContent = data.path + ' (' + formatSize(data.size) + ')';
                document.getElementById('client-ip').textContent = data.client_ip || 'None';
                
                if (data.mode === 'send') {
                    uploadSection.classList.add('hidden');
                    downloadSection.classList.remove('hidden');
                    curlCmd.textContent = 'curl -O -J "' + window.location.origin + '/api/download"';
                } else {
                    uploadSection.classList.remove('hidden');
                    downloadSection.classList.add('hidden');
                    curlCmd.textContent = 'curl -F "file=@YOUR_FILE" "' + window.location.origin + '/api/upload"';
                }
                
                updateStatus(data.status, data.progress, data.error);
            } catch (e) {
                console.error('Failed to get info:', e);
            }
        }
        
        function connectSSE() {
            if (eventSource) {
                eventSource.close();
            }
            
            eventSource = new EventSource('/api/events');
            
            eventSource.onmessage = (e) => {
                if (e.data.startsWith(':heartbeat')) return;
                
                try {
                    const data = JSON.parse(e.data);
                    updateStatus(data.status, data.progress, data.error);
                    document.getElementById('client-ip').textContent = data.client_ip || 'None';
                    
                    if (data.status === 'transferring') {
                        progressContainer.classList.add('active');
                        progressFill.style.width = data.progress + '%';
                        progressText.textContent = data.progress.toFixed(1) + '% (' + formatSize(data.transferred) + ' / ' + formatSize(data.size) + ')';
                        cancelBtn.classList.remove('hidden');
                    } else if (data.status === 'completed') {
                        progressFill.style.width = '100%';
                        progressText.textContent = '100% - Complete!';
                        cancelBtn.classList.add('hidden');
                    }
                } catch (e) {
                    console.error('Failed to parse SSE data:', e);
                }
            };
            
            eventSource.onerror = () => {
                console.log('SSE connection lost, retrying...');
                setTimeout(connectSSE, 1000);
            };
        }
        
        async function fetchLogs() {
            try {
                const response = await fetch('/api/log');
                const logs = await response.json();
                renderLogs(logs);
            } catch (e) {
                console.error('Failed to fetch logs:', e);
            }
        }
        
        function renderLogs(logs) {
            logEntries.innerHTML = logs.map(log => 
                '<div class="log-entry">' + escapeHtml(log) + '</div>'
            ).join('');
            logEntries.scrollTop = logEntries.scrollHeight;
        }
        
        function updateStatus(status, progress, error) {
            statusEl.className = 'status ' + status;
            
            switch(status) {
                case 'waiting':
                    statusEl.textContent = '⏳ Waiting for connection...';
                    break;
                case 'transferring':
                    statusEl.textContent = '📤 Transferring... ' + progress.toFixed(1) + '%';
                    break;
                case 'completed':
                    statusEl.textContent = '✅ Transfer completed!';
                    break;
                case 'cancelled':
                    statusEl.textContent = '❌ Transfer cancelled';
                    break;
                case 'error':
                    statusEl.textContent = '⚠️ Error: ' + (error || 'Unknown error');
                    break;
            }
        }
        
        function formatSize(bytes) {
            if (bytes === 0) return '0 B';
            const k = 1024;
            const sizes = ['B', 'KB', 'MB', 'GB'];
            const i = Math.floor(Math.log(bytes) / Math.log(k));
            return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
        }
        
        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }
        
        // File upload
        dropZone.addEventListener('click', () => fileInput.click());
        
        dropZone.addEventListener('dragover', (e) => {
            e.preventDefault();
            dropZone.classList.add('dragover');
        });
        
        dropZone.addEventListener('dragleave', () => {
            dropZone.classList.remove('dragover');
        });
        
        dropZone.addEventListener('drop', (e) => {
            e.preventDefault();
            dropZone.classList.remove('dragover');
            const files = e.dataTransfer.files;
            if (files.length > 0) {
                uploadFile(files[0]);
            }
        });
        
        fileInput.addEventListener('change', (e) => {
            if (e.target.files.length > 0) {
                uploadFile(e.target.files[0]);
            }
        });
        
        async function uploadFile(file) {
            const formData = new FormData();
            formData.append('file', file);
            
            progressContainer.classList.add('active');
            cancelBtn.classList.remove('hidden');
            
            try {
                const response = await fetch('/api/upload', {
                    method: 'POST',
                    body: formData
                });
                
                if (response.status === 409) {
                    const data = await response.json();
                    if (confirm('File "' + file.name + '" already exists. Overwrite?')) {
                        // TODO: Implement overwrite
                        alert('Please rename the file or choose a different name');
                    }
                } else if (!response.ok) {
                    const text = await response.text();
                    throw new Error(text);
                }
            } catch (e) {
                console.error('Upload failed:', e);
                alert('Upload failed: ' + e.message);
            }
        }
        
        // Download
        downloadBtn.addEventListener('click', () => {
            window.location.href = '/api/download';
        });
        
        // Cancel
        cancelBtn.addEventListener('click', async () => {
            try {
                await fetch('/api/cancel', { method: 'POST' });
            } catch (e) {
                console.error('Cancel failed:', e);
            }
        });
        
        // Refresh logs periodically
        setInterval(fetchLogs, 1000);
        
        // Start
        init();
    </script>
</body>
</html>`
