package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jempe/imagesplitter/imageprocessor"
	"github.com/jempe/imagesplitter/internal/jsonlog"
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
	Width        int    `json:"width"`
	MaxImages    int    `json:"max_images"`
	CreateZip    bool   `json:"create_zip"`
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
	flag.BoolVar(&cfg.useCLI, "use-cli", false, "Use command line tools (vips and zip) instead of Go implementation")

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

	// Validate max_images
	if req.MaxImages < 0 {
		errMessage := map[string]string{
			"error": "max_images must be a positive integer",
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

	processor := imageprocessor.Processor{
		OutputBaseDir: cfg.filePath,
		MaxHeight:     cfg.maxHeight,
		UseCLI:        cfg.useCLI,
	}

	// Download and process the image
	result, err := processor.ProcessImage(imageURL, req.ImagesPrefix, req.Width, req.MaxImages, req.CreateZip)
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

// containsOnlyAllowedChars checks if a string contains only characters from the allowed set
func containsOnlyAllowedChars(s, allowed string) bool {
	for _, char := range s {
		if !strings.ContainsRune(allowed, char) {
			return false
		}
	}
	return true
}

func apiResponse(w http.ResponseWriter, status int, message any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(message)
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
