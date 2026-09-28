package middleware

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Normalize form models without losing uploaded parts. Reusable storage is
// replaced as well, so pass-through adaptors cannot forward routing suffixes.
func resolveFormRoutingModel(c *gin.Context, name string) (string, error) {
	base, err := service.ResolveModelRouting(c, name)
	if err != nil || base == name {
		return base, err
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", err
	}
	source, err := storage.Bytes()
	if err != nil {
		return "", err
	}
	media, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil {
		return "", err
	}
	var body []byte
	switch media {
	case gin.MIMEPOSTForm:
		values, err := url.ParseQuery(string(source))
		if err != nil {
			return "", err
		}
		if len(values["model"]) != 1 {
			return "", fmt.Errorf("model must be provided once")
		}
		values.Set("model", base)
		body = []byte(values.Encode())
	case gin.MIMEMultipartPOSTForm:
		reader := multipart.NewReader(bytes.NewReader(source), params["boundary"])
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		if err = writer.SetBoundary(params["boundary"]); err != nil {
			return "", err
		}
		count := 0
		for {
			part, readErr := reader.NextPart()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return "", readErr
			}
			destination, writeErr := writer.CreatePart(part.Header)
			if writeErr != nil {
				return "", writeErr
			}
			if part.FormName() == "model" && part.FileName() == "" {
				count++
				_, err = io.WriteString(destination, base)
			} else {
				_, err = io.Copy(destination, part)
			}
			if err != nil {
				return "", err
			}
			if err = part.Close(); err != nil {
				return "", err
			}
		}
		if count != 1 {
			return "", fmt.Errorf("model must be provided once")
		}
		if err = writer.Close(); err != nil {
			return "", err
		}
		body = buffer.Bytes()
	default:
		return "", fmt.Errorf("routing rules require a JSON, form or multipart request")
	}
	replacement, err := common.CreateBodyStorage(body)
	if err != nil {
		return "", err
	}
	_ = storage.Close()
	c.Set(common.KeyBodyStorage, replacement)
	c.Set(common.KeyRequestBody, nil)
	c.Request.Body = io.NopCloser(replacement)
	c.Request.ContentLength = replacement.Size()
	if c.Request.PostForm != nil {
		c.Request.PostForm.Set("model", base)
	}
	if c.Request.Form != nil {
		c.Request.Form.Set("model", base)
	}
	if c.Request.MultipartForm != nil {
		c.Request.MultipartForm.Value["model"] = []string{base}
	}
	return base, nil
}
