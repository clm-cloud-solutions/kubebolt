package api

import (
	"log/slog"
	"net/http"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// CORSMiddleware returns a CORS handler for the given allowed origins.
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	return cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Requested-With"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	})
}

// LoggingMiddleware emits a structured access log for each request.
// Uses chi's WrapResponseWriter so SSE (Flusher) and WebSocket upgrades
// (Hijacker) keep working — our wrapper only inspects status and bytes.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		lvl := slog.LevelInfo
		switch {
		case status >= 500:
			lvl = slog.LevelError
		case status >= 400:
			lvl = slog.LevelWarn
		}

		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Int("bytes", ww.BytesWritten()),
			slog.Duration("duration", time.Since(start)),
			slog.String("remote", r.RemoteAddr),
		}
		if reqID := chimiddleware.GetReqID(r.Context()); reqID != "" {
			attrs = append(attrs, slog.String("reqID", reqID))
		}
		if uid := auth.ContextUserID(r); uid != "" {
			attrs = append(attrs, slog.String("user", uid))
		}
		slog.LogAttrs(r.Context(), lvl, "http", attrs...)
	})
}

// JSONContentType sets JSON content type on responses.
func JSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
