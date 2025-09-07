package imageprocessor

import (
	"archive/zip"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Processor struct {
	OutputBaseDir string
	MaxHeight     int
	UseCLI        bool
}

type ImageResponse struct {
	Status  string   `json:"status"`
	Message string   `json:"message"`
	ZipURL  string   `json:"zipUrl"`
	Images  []string `json:"images"`
}

func (p *Processor) ProcessImage(url string, imagesPrefix string, width int, maxImages int) (ImageResponse, error) {
	// Create output directory for image processing
	outputBaseDir := p.OutputBaseDir

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

	// Download image using appropriate method based on config
	var downloadErr error
	if p.UseCLI {
		// Use curl for CLI mode
		downloadErr = downloadImageWithCurl(url, tempImagePath)
	} else {
		// Use Go's HTTP client for Go mode
		downloadErr = downloadImage(url, tempImagePath)
	}

	if downloadErr != nil {
		return ImageResponse{}, downloadErr
	}

	var result ImageResponse
	var err error

	// Choose implementation based on config
	if p.UseCLI {
		// Use command line tools (convert and zip)
		result, err = p.processImageWithCLI(tempImagePath, outputDir, imagesPrefix, width, maxImages)
	} else {
		// Use Go implementation
		result, err = p.processImageWithGo(tempImagePath, outputDir, imagesPrefix, width, maxImages)
	}

	if err != nil {
		return ImageResponse{}, err
	}

	return result, nil
}

// downloadImageWithCurl downloads an image from a URL to a local file using curl
func downloadImageWithCurl(url string, outputPath string) error {
	// Use curl to download the image
	curlCmd := exec.Command(
		"curl",
		"--silent",             // Don't show progress meter or error messages
		"--show-error",         // Show error messages
		"--fail",               // Fail silently on server errors
		"--output", outputPath, // Output to file
		url,
	)

	output, err := curlCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to download image with curl: %v - %s", err, string(output))
	}

	// Check if file exists and has content
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("failed to verify downloaded file: %v", err)
	}

	if fileInfo.Size() == 0 {
		return fmt.Errorf("downloaded file is empty")
	}

	return nil
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
// processImageWithCLI processes an image using command line tools (vips and zip)
func (p *Processor) processImageWithCLI(imagePath string, outputDir string, imagesPrefix string, requestedWidth int, maxImages int) (ImageResponse, error) {
	// Store paths to split images
	var chunkPaths []string

	// Get image dimensions using vips
	vipsInfoCmd := exec.Command("vipsheader", imagePath)
	output, err := vipsInfoCmd.CombinedOutput()
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to get image dimensions: %v - %s", err, string(output))
	}

	// Parse dimensions from vipsheader output
	// Format example: "cteam_01.jpg: 1170x5000 uchar, 3 bands, srgb, jpegload"
	outputStr := strings.TrimSpace(string(output))

	// Split by colon
	parts := strings.Split(outputStr, ":")
	if len(parts) < 2 {
		return ImageResponse{}, fmt.Errorf("unexpected output format from vipsheader: %s", outputStr)
	}

	// Get the part after the colon and trim spaces
	dimensionPart := strings.TrimSpace(parts[1])

	// Split by space to get the dimensions (first token)
	dimensionTokens := strings.Split(dimensionPart, " ")
	if len(dimensionTokens) < 1 {
		return ImageResponse{}, fmt.Errorf("unexpected dimension format from vipsheader: %s", dimensionPart)
	}

	// Split the dimensions by 'x'
	dimensions := strings.Split(dimensionTokens[0], "x")
	if len(dimensions) != 2 {
		return ImageResponse{}, fmt.Errorf("unexpected dimension format from vipsheader: %s", dimensionTokens[0])
	}

	width, err := strconv.Atoi(dimensions[0])
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to parse image width: %v", err)
	}

	totalHeight, err := strconv.Atoi(dimensions[1])
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to parse image height: %v", err)
	}

	// Determine if we need to crop the width
	originalWidth := width
	cropWidth := false
	if requestedWidth > 0 && originalWidth > requestedWidth {
		width = requestedWidth
		cropWidth = true
	}

	// Calculate number of splits needed
	maxHeight := p.MaxHeight
	splitCount := (totalHeight + maxHeight - 1) / maxHeight // Ceiling division

	// Limit the number of images
	if maxImages > 0 && splitCount > maxImages {
		splitCount = maxImages
	}

	// Split the image using vips
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

		// Use vips to extract a region of the image
		cropHeight := endY - startY

		// Command arguments
		var vipsCmd *exec.Cmd

		if cropWidth {
			// If we need to crop width, use extract area with centered x-offset
			xOffset := 0 //(width - requestedWidth) / 2
			vipsCmd = exec.Command(
				"vips", "crop",
				imagePath,
				outputPath,
				fmt.Sprintf("%d", xOffset), fmt.Sprintf("%d", startY),
				fmt.Sprintf("%d", requestedWidth), fmt.Sprintf("%d", cropHeight),
			)
		} else {
			// Use original width
			vipsCmd = exec.Command(
				"vips", "crop",
				imagePath,
				outputPath,
				"0", fmt.Sprintf("%d", startY),
				fmt.Sprintf("%d", width), fmt.Sprintf("%d", cropHeight),
			)
		}

		output, err := vipsCmd.CombinedOutput()
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

	images := []string{}

	// Add all image paths to the zip command
	for _, imagePath := range chunkPaths {
		zipArgs = append(zipArgs, imagePath)

		imageRelPath, _ := filepath.Rel(p.OutputBaseDir, imagePath)
		images = append(images, imageRelPath)
	}

	// Execute the zip command
	zipCmd := exec.Command("zip", zipArgs...)
	output, err = zipCmd.CombinedOutput()
	if err != nil {
		return ImageResponse{}, fmt.Errorf("failed to create zip file: %v - %s", err, string(output))
	}

	// Get absolute path to zip file
	absZipPath, _ := filepath.Abs(zipFileName)

	relativeZipPath, _ := filepath.Rel(p.OutputBaseDir, absZipPath)

	return ImageResponse{
		Status:  "success",
		Message: fmt.Sprintf("Successfully split image into %d parts and created zip file using CLI tools", splitCount),
		ZipURL:  relativeZipPath,
		Images:  images,
	}, nil
}

func (p *Processor) processImageWithGo(imagePath string, outputDir string, imagesPrefix string, requestedWidth int, maxImages int) (ImageResponse, error) {
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
	originalWidth := bounds.Max.X
	totalHeight := bounds.Max.Y

	// Determine if we need to crop the width
	width := originalWidth
	cropWidth := false
	if requestedWidth > 0 && originalWidth > requestedWidth {
		width = requestedWidth
		cropWidth = true
	}

	// Calculate number of splits needed
	maxHeight := p.MaxHeight
	splitCount := (totalHeight + maxHeight - 1) / maxHeight // Ceiling division

	// Limit the number of images
	if maxImages > 0 && splitCount > maxImages {
		splitCount = maxImages
	}

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
				// If cropping width, center the image horizontally
				srcX := x
				if cropWidth {
					// Calculate offset to center the cropped area
					offset := 0 //(originalWidth - width) / 2
					srcX = x + offset
				}
				subImg.Set(x, y-startY, img.At(srcX, y))
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

	images := []string{}

	// Add each split image to the zip file
	for _, imagePath := range chunkPaths {
		if err := addFileToZip(zipWriter, imagePath); err != nil {
			return ImageResponse{}, fmt.Errorf("failed to add file to zip: %v", err)
		}

		imageRelPath, _ := filepath.Rel(p.OutputBaseDir, imagePath)
		images = append(images, imageRelPath)
	}

	// Close the zip writer before returning
	if err := zipWriter.Close(); err != nil {
		return ImageResponse{}, fmt.Errorf("failed to close zip writer: %v", err)
	}

	// Get absolute path to zip file
	absZipPath, _ := filepath.Abs(zipFileName)

	relativeZipPath, _ := filepath.Rel(p.OutputBaseDir, absZipPath)

	return ImageResponse{
		Status:  "success",
		Message: fmt.Sprintf("Successfully split image into %d parts and created zip file", splitCount),
		ZipURL:  relativeZipPath,
		Images:  images,
	}, nil
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
