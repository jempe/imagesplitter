# ImageSplitter

ImageSplitter is a Go-based web service that splits large images into smaller chunks and provides them as a downloadable ZIP file. This is particularly useful for processing very tall images that need to be divided into manageable pieces.

## Features

- Split images into multiple parts based on a configurable maximum height
- Create ZIP archives containing all image chunks
- Support for both JPEG and PNG image formats
- Optional basic authentication
- RESTful API interface

## Installation

### Prerequisites

- Go 1.21 or higher
- Write access to the file system for storing processed images

### Installation Steps

1. Clone the repository:
   ```bash
   git clone https://github.com/jempe/ImageSplitter.git
   cd ImageSplitter
   ```

2. Build the application:
   ```bash
   go build -o imagesplitter
   ```

## Usage

### Running the Server

Start the server with the required configuration flags:

```bash
./imagesplitter --port=8081 --url-host="https://example.com/" --file-path="/path/to/storage/"
```

### Required Flags

- `--url-host`: Base URL where images can be accessed (must start with http:// or https:// and end with a slash)
- `--file-path`: Directory path where processed images will be stored (must start and end with a slash)

### Optional Flags

- `--port`: Server port (default: 4000)
- `--username`: Username for basic authentication (if not provided, authentication is disabled)
- `--password`: Password for basic authentication
- `--max-height`: Maximum height for image chunks in pixels (default: 5000)

## API Endpoints

### Split Image

**Endpoint:** `/split-image`

**Method:** POST

**Authentication:** Basic Auth (if configured)

**Request Body:**
```json
{
  "url": "path/to/image.jpg",
  "images_prefix": "page"
}
```

- `url`: Path to the image (relative to the url-host)
- `images_prefix`: Prefix for the generated image files (must contain only alphanumeric characters and underscores)

**Response:**
```json
{
  "status": "success",
  "message": "Successfully split image into 3 parts and created zip file",
  "zipUrl": "/absolute/path/to/zip/file.zip"
}
```

## Examples

### Example Request

```bash
curl -X POST http://localhost:8081/split-image \
  -u username:password \
  -H "Content-Type: application/json" \
  -d '{"url": "images/tall-image.jpg", "images_prefix": "page"}'
```

This will:
1. Download the image from `https://example.com/images/tall-image.jpg`
2. Split it into multiple parts (e.g., page_01.jpg, page_02.jpg, etc.)
3. Create a ZIP file containing all parts
4. Return the path to the ZIP file

## Error Handling

The API returns appropriate HTTP status codes and error messages:

- 400 Bad Request: Invalid request parameters
- 401 Unauthorized: Authentication failure
- 405 Method Not Allowed: Using methods other than POST
- 500 Internal Server Error: Processing errors

## License

[Include license information here]

## Version

Current version: 1.0.0
