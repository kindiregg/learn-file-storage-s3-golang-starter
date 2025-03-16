package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	const maxMemory = 10 << 20 // 10 MB
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	rawContentType := header.Header.Get("Content-Type")
	if rawContentType == "" {
		respondWithError(w, http.StatusBadRequest, "Missing Content-Type for thumbnail", nil)
		return
	}

	mediaType, _, err := mime.ParseMediaType(rawContentType)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error getitng media type", err)
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not authorized to update this video", nil)
		return
	}

	var fileExt string
	switch mediaType {
	case "image/jpeg":
		fileExt = ".jpg"
	case "image/png":
		fileExt = ".png"
	default:
		fileExt = "invalid"
	}

	if fileExt == "invalid" {
		respondWithError(w, http.StatusBadRequest, "Thumbnail must be a JPEG or PNG image", nil)
		return
	}
	bytes := make([]byte, 32)
	_, err = rand.Read(bytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error randomizing bytes", err)
	}
	fakeVideoString := base64.RawURLEncoding.EncodeToString(bytes)

	fileName := fakeVideoString + fileExt

	thumbnailPath := filepath.Join(cfg.assetsRoot, fileName)
	thumbnailFile, err := os.Create(thumbnailPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating file", err)
		return
	}
	defer thumbnailFile.Close()
	_, err = io.Copy(thumbnailFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error writing file", err)
		return
	}

	thumnailFileURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, fileName)

	video.ThumbnailURL = &thumnailFileURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
