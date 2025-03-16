package main

import (
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

	uploadFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error creating temporary file", err)
		return
	}
	defer os.Remove(uploadFile.Name())
	defer uploadFile.Close()

	_, err = io.Copy(uploadFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error copying data to upload file", err)
		return
	}

	_, err = uploadFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error resetting upload file pointer", err)
		return
	}

	fileKey := getAssetPath(mediaType)

	putObjectInput := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(fileKey),
		Body:        uploadFile,
		ContentType: aws.String(mediaType),
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
	}

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "error updating video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"video_url": *video.VideoURL})
}
