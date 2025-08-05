package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jempe/ImageSplitter/internal/jsonlog"
)

const version = "1.0.0"

type config struct {
	port     int
	urlHost  string
	filePath string
}

type ImageRequest struct {
	URL string `json:"url"`
}

var logger *jsonlog.Logger
var cfg config

func main() {
	logger = jsonlog.New(os.Stdout, jsonlog.LevelInfo)

	// API Web Server Settings
	flag.IntVar(&cfg.port, "port", 4000, "API server port")

	flag.StringVar(&cfg.urlHost, "url-host", "", "Base path for image processing")
	flag.StringVar(&cfg.filePath, "file-path", "", "File path for image processing")

	flag.Parse()

	if cfg.urlHost == "" || cfg.filePath == "" {
		logger.PrintFatal(errors.New("url host and file path cannot be empty"), nil)
	}

	if (strings.HasPrefix(cfg.urlHost, "http://") || strings.HasPrefix(cfg.urlHost, "https://")) == false {
		logger.PrintFatal(errors.New("url host must start with http:// or https://"), nil)
	}

	if strings.HasSuffix(cfg.urlHost, "/") == false {
		logger.PrintFatal(errors.New("url host must end with a slash"), nil)
	}

	if !(strings.HasSuffix(cfg.filePath, "/") && strings.HasPrefix(cfg.filePath, "/")) {
		logger.PrintFatal(errors.New("file path must start and end with a slash"), nil)
	}

	if !checkIfFileExists(cfg.filePath) {
		logger.PrintFatal(errors.New("file path does not exist"), nil)
	}

	if !checkIfIsDirectory(cfg.filePath) {
		logger.PrintFatal(errors.New("file path is not a directory"), nil)
	}

	if !checkIfIsWritable(cfg.filePath) {
		logger.PrintFatal(errors.New("file path is not writable"), nil)
	}

	http.HandleFunc("/split-image", handleSplitImage)
	logger.PrintInfo("Starting server", map[string]string{
		"port":      fmt.Sprintf("%d", cfg.port),
		"url-host":  cfg.urlHost,
		"file-path": cfg.filePath,
	})

	logger.PrintFatal(http.ListenAndServe(":8081", nil), nil)
}

func handleSplitImage(w http.ResponseWriter, r *http.Request) {
	// Only allow POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse JSON request
	var req ImageRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	imageURL := cfg.urlHost + req.URL

	// Download and process the image
	result, err := processImage(imageURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return success response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": result,
	})
}

func processImage(url string) (string, error) {
	// Create output directory for image processing
	outputBaseDir := cfg.filePath

	// Create a unique directory name based on timestamp
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	outputDir := filepath.Join(outputBaseDir, timestamp+"_"+filepath.Base(url))

	// Create the directories
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %v", err)
	}

	// Store paths to split images
	var chunkPaths []string

	// Download image using streaming
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download image: %v", err)
	}
	defer resp.Body.Close()

	// Read image
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to decode image: %v", err)
	}

	// Get image dimensions
	bounds := img.Bounds()
	width := bounds.Max.X
	totalHeight := bounds.Max.Y

	// Calculate number of splits needed
	maxHeight := 5000
	splitCount := (totalHeight + maxHeight - 1) / maxHeight // Ceiling division

	// Output directory is already created

	// Split the image
	for i := 0; i < splitCount; i++ {
		startY := i * maxHeight
		endY := startY + maxHeight
		if endY > totalHeight {
			endY = totalHeight
		}

		// Create subimage
		subImg := image.NewRGBA(image.Rect(0, 0, width, endY-startY))
		for y := startY; y < endY; y++ {
			for x := 0; x < width; x++ {
				subImg.Set(x, y-startY, img.At(x, y))
			}
		}

		// Save the split image
		// Add leading zero for numbers less than 10
		fileNumber := i + 1
		fileNumberStr := fmt.Sprintf("%d", fileNumber)
		if fileNumber < 10 {
			fileNumberStr = fmt.Sprintf("0%d", fileNumber)
		}
		outputPath := filepath.Join(outputDir, fmt.Sprintf("split_%s.jpg", fileNumberStr))
		outFile, err := os.Create(outputPath)
		if err != nil {
			return "", fmt.Errorf("failed to create output file: %v", err)
		}

		if strings.HasSuffix(strings.ToLower(url), ".png") {
			if err := png.Encode(outFile, subImg); err != nil {
				outFile.Close()
				return "", fmt.Errorf("failed to save split image: %v", err)
			}
		} else {
			// Default to JPEG
			if err := jpeg.Encode(outFile, subImg, &jpeg.Options{Quality: 90}); err != nil {
				outFile.Close()
				return "", fmt.Errorf("failed to save split image: %v", err)
			}
		}
		outFile.Close()

		// Add absolute path to response
		absPath, _ := filepath.Abs(outputPath)
		chunkPaths = append(chunkPaths, absPath)
	}

	// Create a formatted list of file paths
	var fileList string
	for i, path := range chunkPaths {
		fileList += fmt.Sprintf("\n%d. %s", i+1, path)
	}

	return fmt.Sprintf("Successfully split image into %d parts.\nFiles saved in: %s%s", splitCount, outputDir, fileList), nil
}

func checkIfFileExists(file string) bool {
	_, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}

func checkIfIsDirectory(file string) bool {
	fileInfo, err := os.Stat(file)
	if err != nil {
		return false
	}
	return fileInfo.IsDir()
}

func checkIfIsWritable(file string) bool {
	fileInfo, err := os.Stat(file)
	if err != nil {
		return false
	}
	return fileInfo.Mode().Perm()&(1<<2) != 0
}
