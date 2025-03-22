package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const UPLOADSIZELIMIT = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, UPLOADSIZELIMIT)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	accessToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't find token", err)
		return
	}

	userID, err := auth.ValidateJWT(accessToken, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't get video", err)
		return
	}

	if userID != video.UserID {
		respondWithError(w, http.StatusUnauthorized, "You don't own this video", nil)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error forming video file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil || mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "error parsing mp4 mime type", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error creating temporary file", err)
		return
	}

	defer tempFile.Close()
	defer os.Remove(tempFile.Name())

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error copying data to upload file", err)
		return
	}

	tempFile.Sync()

	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error resetting upload file pointer", err)
		return
	}

	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error processing video", err)
		return
	}
	defer os.Remove(processedFilePath)

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error opening processed video file", err)
		return
	}

	defer processedFile.Close()

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error getting file aspect ratio", err)
	}
	var ratioPrefix string
	if aspectRatio == "16:9" {
		ratioPrefix = "landscape"
	} else if aspectRatio == "9:16" {
		ratioPrefix = "portrait"
	} else {
		ratioPrefix = "other"
	}

	fileKey := getAssetPath(mediaType)
	fileKey = filepath.Join(ratioPrefix, fileKey)

	putObjectInput := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(fileKey),
		Body:        processedFile,
		ContentType: aws.String(mediaType),
		ACL:         types.ObjectCannedACLPublicRead,
	}

	_, err = cfg.s3client.PutObject(r.Context(), putObjectInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video URL", err)
		return
	}

	_, err = cfg.s3client.HeadObject(r.Context(), &s3.HeadObjectInput{
		Bucket: &cfg.s3Bucket,
		Key:    &fileKey,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error verifying S3 upload", err)
		return
	}

	s3VideoURL := cfg.getObjectURL(fileKey)
	video.VideoURL = &s3VideoURL

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "error updating video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"video_url": *video.VideoURL})
}

func getVideoAspectRatio(filePath string) (string, error) {
	log.Printf("Analyzing file: %s", filePath)
	var stdoutBuffer, stderrBuffer bytes.Buffer
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	cmd.Stdout = &stdoutBuffer
	cmd.Stderr = &stderrBuffer

	err := cmd.Run()
	if err != nil {
		log.Printf("ffprobe error: %v\nstderr: %s\n", err, stderrBuffer.String())
		return "", err
	}

	var fileData struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(stdoutBuffer.Bytes(), &fileData); err != nil {
		return "", fmt.Errorf("could not parse ffprobe output: %v", err)
	}

	if len(fileData.Streams) == 0 {
		return "", fmt.Errorf("no streams found in video file")
	}

	ratio := float64(fileData.Streams[0].Width) / float64(fileData.Streams[0].Height)
	log.Printf("Width: %d, Height: %d, Calculated Ratio: %f", fileData.Streams[0].Width, fileData.Streams[0].Height, ratio)
	if ratio >= 1.77 && ratio <= 1.78 { // Roughly 16:9 allowing for inexact ratio
		return "16:9", nil
	} else if ratio >= 0.56 && ratio <= 0.57 { // Roughly 9:16 allowing for inexact ratio
		return "9:16", nil
	} else {
		return "other", nil
	}
}

func processVideoForFastStart(filePath string) (string, error) {
	var stderrBuffer bytes.Buffer
	outputPath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i",
		filePath, "-c",
		"copy", "-movflags",
		"faststart", "-f", "mp4", outputPath)
	cmd.Stderr = &stderrBuffer

	err := cmd.Run()
	if err != nil {
		log.Printf("ffmpeg error: %v\nstderr: %s\n", err, stderrBuffer.String())
		return "", err
	}
	return outputPath, nil
}
