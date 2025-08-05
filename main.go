package main

import (
	"archive/zip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jempe/ImageSplitter/internal/jsonlog"
)

const version = "1.0.0"

type config struct {
	port      int
	urlHost   string
	filePath  string
	username  string
	password  string
	maxHeight int
	useCLI    bool
}

type ImageRequest struct {
	URL          string `json:"url"`
	ImagesPrefix string `json:"images_prefix"`
}

type ImageResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	ZipURL  string `json:"zipUrl"`
}

var logger *jsonlog.Logger
var cfg config
var wg sync.WaitGroup

func main() {
	logger = jsonlog.New(os.Stdout, jsonlog.LevelInfo)

	// API Web Server Settings
	flag.IntVar(&cfg.port, "port", 4000, "API server port")

	flag.StringVar(&cfg.urlHost, "url-host", "", "Base path for image processing")
	flag.StringVar(&cfg.filePath, "file-path", "", "File path for image processing")

	// Authentication settings
	flag.StringVar(&cfg.username, "username", "", "Username for basic authentication")
	flag.StringVar(&cfg.password, "password", "", "Password for basic authentication")

	// Image processing settings
	flag.IntVar(&cfg.maxHeight, "max-height", 5000, "Maximum height for image processing")

	// Implementation selection
	flag.BoolVar(&cfg.useCLI, "use-cli", false, "Use command line tools (convert and zip) instead of Go implementation")

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

	// Wrap the handler with basic authentication if credentials are provided
	if cfg.username != "" && cfg.password != "" {
		logger.PrintInfo("Basic authentication enabled", nil)
		http.HandleFunc("/split-image", basicAuth(handleSplitImage))
	} else {
		logger.PrintInfo("Basic authentication disabled", nil)
		http.HandleFunc("/split-image", handleSplitImage)
	}
	logger.PrintInfo("Starting server", map[string]string{
		"port":      fmt.Sprintf("%d", cfg.port),
		"url-host":  cfg.urlHost,
		"file-path": cfg.filePath,
		"use-cli":   fmt.Sprintf("%t", cfg.useCLI),
	})

	err := serve()
	if err != nil {
		logger.PrintFatal(err, nil)
	}
}

// basicAuth is a middleware that wraps an http.HandlerFunc with basic authentication
func basicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get credentials from the request header
		username, password, ok := r.BasicAuth()
		if !ok {
			// No credentials provided, return 401 Unauthorized
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check if credentials are valid using constant-time comparison to prevent timing attacks
		usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(cfg.username)) == 1
		passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.password)) == 1

		if !usernameMatch || !passwordMatch {
			// Invalid credentials, return 401 Unauthorized
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Credentials are valid, call the next handler
		next(w, r)
	}
}

func handleSplitImage(w http.ResponseWriter, r *http.Request) {
	// Only allow POST requests
	if r.Method != http.MethodPost {
		errMessage := map[string]string{
			"error": "Method not allowed",
		}
		apiResponse(w, http.StatusMethodNotAllowed, errMessage)
		return
	}

	// Parse JSON request
	var req ImageRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		errMessage := map[string]string{
			"error": "Invalid JSON",
		}
		apiResponse(w, http.StatusBadRequest, errMessage)
		return
	}

	// Validate URL
	if req.URL == "" {
		errMessage := map[string]string{
			"error": "URL is required",
		}
		apiResponse(w, http.StatusBadRequest, errMessage)
		return
	}

	// Validate images_prefix
	if req.ImagesPrefix == "" {
		errMessage := map[string]string{
			"error": "images_prefix is required",
		}
		apiResponse(w, http.StatusBadRequest, errMessage)
		return
	}

	// Validate images_prefix contains only alphanumeric characters and underscores
	if !containsOnlyAllowedChars(req.ImagesPrefix, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") {
		errMessage := map[string]string{
			"error": "images_prefix contains invalid characters",
		}
		apiResponse(w, http.StatusBadRequest, errMessage)
		return
	}

	imageURL := cfg.urlHost + req.URL

	// Download and process the image
	result, err := processImage(imageURL, req.ImagesPrefix)
	if err != nil {
		errMessage := map[string]string{
			"error": err.Error(),
		}
		apiResponse(w, http.StatusInternalServerError, errMessage)
		return
	}

	// Return success response
	apiResponse(w, http.StatusOK, result)
}

func apiResponse(w http.ResponseWriter, status int, message any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(message)
}

func processImage(url string, imagesPrefix string) (ImageResponse, error) {
	// Create output directory for image processing
	outputBaseDir := cfg.filePath

	// Create a unique directory name based on timestamp
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	outputDir := filepath.Join(outputBaseDir, timestamp)

	// Create the directories
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return ImageResponse{}, fmt.Errorf("failed to create output directory: %v", err)
	}

	// Download the image to a temporary file
	tempImagePath := filepath.Join(outputDir, "original_image")
	// Determine file extension from URL
	fileExt := ".jpg" // Default
	if strings.HasSuffix(strings.ToLower(url), ".png") {
		fileExt = ".png"
	}
	tempImagePath = tempImagePath + fileExt

	// Download image
	if err := downloadImage(url, tempImagePath); err != nil {
		return ImageResponse{}, err
	}

	var result ImageResponse
	var err error

	// Choose implementation based on config
	if cfg.useCLI {
		// Use command line tools (convert and zip)
		result, err = processImageWithCLI(tempImagePath, outputDir, imagesPrefix)
	} else {
		// Use Go implementation
		result, err = processImageWithGo(tempImagePath, outputDir, imagesPrefix)
	}

	if err != nil {
		return ImageResponse{}, err
	}

	return result, nil
}

// downloadImage downloads an image from a URL to a local file
func downloadImage(url string, outputPath string) error {
	// Download image using streaming
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download image: %v", err)
	}
	defer resp.Body.Close()

	// Create output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer outFile.Close()

	// Copy data from response to file
	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save image: %v", err)
	}

	return nil
}

// processImageWithGo processes an image using Go's image processing libraries
// processImageWithCLI processes an image using command line tools (convert and zip)
func processImageWithCLI(imagePath string, outputDir string, imagesPrefix string) (ImageResponse, error) {
	// Store paths to split images
	var chunkPaths []string

	// Get image dimensions using ImageMagick's identify command
	identifyCmd := exec.Command("identify", "-format", "%w %h", imagePath)
	output, err := identifyCmd.CombinedOutput()
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to get image dimensions: %v - %s", err, string(output))
	}

	// Parse dimensions
	dimensions := strings.Split(strings.TrimSpace(string(output)), " ")
	if len(dimensions) != 2 {
		return ImageResponse{}, fmt.Errorf("unexpected output from identify command: %s", string(output))
	}

	width, err := strconv.Atoi(dimensions[0])
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to parse image width: %v", err)
	}

	totalHeight, err := strconv.Atoi(dimensions[1])
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to parse image height: %v", err)
	}

	// Calculate number of splits needed
	maxHeight := cfg.maxHeight
	splitCount := (totalHeight + maxHeight - 1) / maxHeight // Ceiling division

	// Split the image using ImageMagick's convert command
	for i := 0; i < splitCount; i++ {
		startY := i * maxHeight
		endY := startY + maxHeight
		if endY > totalHeight {
			endY = totalHeight
		}

		// Add leading zero for numbers less than 10
		fileNumber := i + 1
		fileNumberStr := fmt.Sprintf("%d", fileNumber)
		if fileNumber < 10 {
			fileNumberStr = fmt.Sprintf("0%d", fileNumber)
		}

		// Output path for this split
		outputPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s.jpg", imagesPrefix, fileNumberStr))

		// Use convert to crop the image
		cropHeight := endY - startY
		convertCmd := exec.Command(
			"convert",
			imagePath,
			"-crop", fmt.Sprintf("%dx%d+0+%d", width, cropHeight, startY),
			outputPath,
		)

		output, err := convertCmd.CombinedOutput()
		if err != nil {
			return ImageResponse{}, fmt.Errorf("failed to split image: %v - %s", err, string(output))
		}

		// Add absolute path to response
		absPath, _ := filepath.Abs(outputPath)
		chunkPaths = append(chunkPaths, absPath)
	}

	// Create a zip file using the zip command
	zipFileName := filepath.Join(outputDir, fmt.Sprintf("%s.zip", imagesPrefix))

	// No need to change directories, we'll use absolute paths

	// Create the zip command with all image files
	zipArgs := []string{
		"-j", // Store just the name of the file (junk the path)
		zipFileName,
	}

	// Add all image paths to the zip command
	for _, imagePath := range chunkPaths {
		zipArgs = append(zipArgs, imagePath)
	}

	// Execute the zip command
	zipCmd := exec.Command("zip", zipArgs...)
	output, err = zipCmd.CombinedOutput()
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to create zip file: %v - %s", err, string(output))
	}

	// Get absolute path to zip file
	absZipPath, _ := filepath.Abs(zipFileName)

	relativeZipPath, _ := filepath.Rel(cfg.filePath, absZipPath)

	return ImageResponse{
		Status:  "success",
		Message: fmt.Sprintf("Successfully split image into %d parts and created zip file using CLI tools", splitCount),
		ZipURL:  relativeZipPath,
	}, nil
}

func processImageWithGo(imagePath string, outputDir string, imagesPrefix string) (ImageResponse, error) {
	// Store paths to split images
	var chunkPaths []string

	// Open the image file
	file, err := os.Open(imagePath)
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to open image file: %v", err)
	}
	defer file.Close()

	// Decode the image
	img, _, err := image.Decode(file)
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to decode image: %v", err)
	}

	// Get image dimensions
	bounds := img.Bounds()
	width := bounds.Max.X
	totalHeight := bounds.Max.Y

	// Calculate number of splits needed
	maxHeight := cfg.maxHeight
	splitCount := (totalHeight + maxHeight - 1) / maxHeight // Ceiling division

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
		outputPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s.jpg", imagesPrefix, fileNumberStr))
		outFile, err := os.Create(outputPath)
		if err != nil {
			return ImageResponse{}, fmt.Errorf("failed to create output file: %v", err)
		}

		if strings.HasSuffix(strings.ToLower(imagePath), ".png") {
			if err := png.Encode(outFile, subImg); err != nil {
				outFile.Close()
				return ImageResponse{}, fmt.Errorf("failed to save split image: %v", err)
			}
		} else {
			// Default to JPEG
			if err := jpeg.Encode(outFile, subImg, &jpeg.Options{Quality: 90}); err != nil {
				outFile.Close()
				return ImageResponse{}, fmt.Errorf("failed to save split image: %v", err)
			}
		}
		outFile.Close()

		// Add absolute path to response
		absPath, _ := filepath.Abs(outputPath)
		chunkPaths = append(chunkPaths, absPath)
	}

	// Create a zip file containing all the split images
	zipFileName := filepath.Join(outputDir, fmt.Sprintf("%s.zip", imagesPrefix))
	zipFile, err := os.Create(zipFileName)
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to create zip file: %v", err)
	}
	defer zipFile.Close()

	// Create a new zip archive
	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	// Add each split image to the zip file
	for _, imagePath := range chunkPaths {
		if err := addFileToZip(zipWriter, imagePath); err != nil {
			return ImageResponse{}, fmt.Errorf("failed to add file to zip: %v", err)
		}
	}

	// Close the zip writer before returning
	if err := zipWriter.Close(); err != nil {
		return ImageResponse{}, fmt.Errorf("failed to close zip writer: %v", err)
	}

	// Get absolute path to zip file
	absZipPath, _ := filepath.Abs(zipFileName)

	relativeZipPath, _ := filepath.Rel(cfg.filePath, absZipPath)

	return ImageResponse{
		Status:  "success",
		Message: fmt.Sprintf("Successfully split image into %d parts and created zip file", splitCount),
		ZipURL:  relativeZipPath,
	}, nil
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

// containsOnlyAllowedChars checks if a string contains only characters from the allowed set
func containsOnlyAllowedChars(s, allowed string) bool {
	for _, char := range s {
		if !strings.ContainsRune(allowed, char) {
			return false
		}
	}
	return true
}

// addFileToZip adds a file to a zip archive
func addFileToZip(zipWriter *zip.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Get file information
	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Create a header for the file
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	// Use base name of file as name in the archive
	header.Name = filepath.Base(filePath)

	// Set compression method
	header.Method = zip.Deflate

	// Create writer for the file in the archive
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	// Copy file contents to the archive
	_, err = io.Copy(writer, file)
	return err
}

func serve() error {
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.port),
		Handler:      nil,
		IdleTimeout:  time.Minute,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	shutdownError := make(chan error)

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		s := <-quit

		logger.PrintInfo("caught signal", map[string]string{
			"signal": s.String(),
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := srv.Shutdown(ctx)
		if err != nil {
			shutdownError <- err
		}

		logger.PrintInfo("completing background tasks", map[string]string{
			"addr": srv.Addr,
		})

		wg.Wait()
		shutdownError <- nil
	}()

	logger.PrintInfo("starting server", map[string]string{
		"addr": srv.Addr,
	})

	err := srv.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	err = <-shutdownError
	if err != nil {
		return err
	}

	logger.PrintInfo("stopped server", map[string]string{
		"addr":    srv.Addr,
		"version": version,
	})

	return nil
}
