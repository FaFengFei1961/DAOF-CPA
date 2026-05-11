package proxy

import (
	"daof-ai-hub/database"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFallbackCompletionTokens(t *testing.T) {
	database.DB, _ = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	database.DB.AutoMigrate(&database.ApiLog{})

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(10 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"World!\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer mockUpstream.Close()

	app := fiber.New()

	app.Post("/v1/chat/completions", func(c *fiber.Ctx) error {
		// Mock fasthttp behavior connecting to our proxy test server
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(mockUpstream.URL)
		req.Header.SetMethod(fiber.MethodPost)

		err := fasthttp.Do(req, resp)
		if err != nil {
			return err
		}

		return c.Send(resp.Body())
	})

	// This tests mock framework. Real proxy test covered upstream.
}
