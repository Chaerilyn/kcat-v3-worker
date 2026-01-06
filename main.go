package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/joho/godotenv" // Import the package
)

// Removed WORKER_SECRET from constants
const (
	MAX_CONCURRENT_JOBS = 1
)

// Global job counter
var requestID int = 0
var jobQueue = make(chan struct{}, MAX_CONCURRENT_JOBS)

func main() {
	// 1. Load .env file
	// Load() looks for .env in the current directory.
	// We ignore the error so this code still works in production (Docker/Cloud) 
	// if variables are set directly in the OS.
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️  No .env file found. Relying on system environment variables.")
	}

	// 2. Health Check
	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		log.Println("🏓 Ping received")
		w.Write([]byte("pong"))
	})

	// 3. Conversion Endpoint
	http.HandleFunc("/convert-webp", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID++
		id := requestID

		log.Printf("[#%d] 📥 New Request received from %s", id, r.RemoteAddr)

		// --- Security ---
		// READ SECRET FROM ENV HERE
		workerSecret := os.Getenv("WORKER_SECRET")
		
		if workerSecret == "" {
			log.Printf("[#%d] ❌ Fatal: WORKER_SECRET is not set in environment", id)
			http.Error(w, "Server Configuration Error", http.StatusInternalServerError)
			return
		}

		if r.Header.Get("Authorization") != "Bearer "+workerSecret {
			log.Printf("[#%d] ⛔ Unauthorized attempt", id)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// --- Queue ---
		if len(jobQueue) >= MAX_CONCURRENT_JOBS {
			log.Printf("[#%d] ⏳ Queue is full. Waiting for slot...", id)
		}

		jobQueue <- struct{}{}
		defer func() { <-jobQueue }()
		
		queueWaitDuration := time.Since(start)
		log.Printf("[#%d] ⚡ Slot acquired (Waited: %v). Reading file...", id, queueWaitDuration)

		// --- File Read ---
		file, header, err := r.FormFile("file")
		if err != nil {
			log.Printf("[#%d] ❌ Error reading form file: %v", id, err)
			http.Error(w, "Failed to read file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		fileSize := header.Size
		log.Printf("[#%d] 📦 File: %s (Size: %.2f MB)", id, header.Filename, float64(fileSize)/1024/1024)

		inputPath := filepath.Join("/tmp", fmt.Sprintf("input_%d_%s", id, header.Filename))
		outFile, _ := os.Create(inputPath)
		io.Copy(outFile, file)
		outFile.Close()
		defer os.Remove(inputPath)

		// --- FFmpeg ---
		outputPath := inputPath + ".webp"
		log.Printf("[#%d] 🎬 Starting FFmpeg conversion...", id)
		ffmpegStart := time.Now()

		cmd := exec.Command("ffmpeg", "-y",
			"-i", inputPath,
			"-t", "30",
			"-c:v", "libwebp",
			"-q:v", "50",
			"-loop", "0",
			"-preset", "default",
			outputPath,
		)

		if output, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[#%d] ❌ FFmpeg Failed: %s", id, string(output))
			http.Error(w, "Conversion failed", http.StatusInternalServerError)
			return
		}
		
		ffmpegDuration := time.Since(ffmpegStart)
		log.Printf("[#%d] ✅ FFmpeg finished in %v. Sending result back...", id, ffmpegDuration)

		// --- Response ---
		stat, _ := os.Stat(outputPath)
		log.Printf("[#%d] 📤 Uploading result (%.2f MB)...", id, float64(stat.Size())/1024/1024)

		w.Header().Set("Content-Type", "image/webp")
		http.ServeFile(w, r, outputPath)
		
		os.Remove(outputPath) 
		
		totalDuration := time.Since(start)
		log.Printf("[#%d] 🎉 Request Complete. Total Time: %v", id, totalDuration)
	})

	log.Println("🚀 Verbose Worker Online on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}